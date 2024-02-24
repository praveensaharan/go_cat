package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var client *redis.Client

func init() {
	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	// Create Redis client
	client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", os.Getenv("REDIS_HOSTNAME"), os.Getenv("REDIS_PORT")),
		Password: os.Getenv("REDIS_PASSWORD"),
	})

	// Ping Redis to check the connection
	pong, err := client.Ping(context.Background()).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Printf("Connected to Redis: %s", pong)
}

type UserData struct {
	Sub      string `json:"user_id"`
	Image    string `json:"picture"`
	Nickname string `json:"nickname"`
	Name     string `json:"name"`
	Score    int    `json:"score"`
}

func getUserDataFromRedis(sub string) (UserData, error) {
	ctx := context.Background() // Create a background context
	redisKey := fmt.Sprintf("user:%s", sub)
	vals, err := client.HGetAll(ctx, redisKey).Result()
	if err != nil {
		return UserData{}, err
	}

	scoreStr, ok := vals["score"]
	if !ok {
		return UserData{}, fmt.Errorf("score not found for user with sub: %s", sub)
	}

	score, err := strconv.Atoi(scoreStr)
	if err != nil {
		return UserData{}, fmt.Errorf("failed to convert score to integer for user with sub: %s", sub)
	}

	userData := UserData{
		Sub:      vals["sub"],
		Image:    vals["image"],
		Nickname: vals["nickname"],
		Name:     vals["name"],
		Score:    score,
	}
	return userData, nil
}

func fetchUserDataFromAPI(sub string) (UserData, error) {
	url := fmt.Sprintf("https://dev-w6w73v6food6memp.us.auth0.com/api/v2/users/%s", sub)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return UserData{}, err
	}
	req.Header.Add("Authorization", "Bearer "+os.Getenv("TOKEN")) // Replace with your actual access token

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return UserData{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return UserData{}, fmt.Errorf("failed to fetch user data: %s", res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return UserData{}, err
	}

	var userData UserData
	if err := json.Unmarshal(body, &userData); err != nil {
		return UserData{}, err
	}

	return userData, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	// Handle CORS
	corsHandler := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			h.ServeHTTP(w, r)
		})
	}

	http.Handle("/", corsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello, the server is running on port %s", port)
	})))

	http.Handle("/user/", corsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := strings.TrimPrefix(r.URL.Path, "/user/")
		if sub == "" {
			http.Error(w, "Sub parameter is required", http.StatusBadRequest)
			return
		}

		userData, err := getUserDataFromRedis(sub)
		if err != nil {
			log.Printf("Error getting user data from Redis for sub %s: %v", sub, err)
			response, err := fetchUserDataFromAPI(sub)
			if err != nil {
				log.Printf("Error fetching user data from API for sub %s: %v", sub, err)
				http.Error(w, "Failed to fetch user data", http.StatusInternalServerError)
				return
			}
			apiUserData := response

			// Store fetched user data in Redis
			redisKey := fmt.Sprintf("user:%s", sub)
			err = client.HMSet(context.Background(), redisKey, map[string]interface{}{
				"sub":      apiUserData.Sub,
				"image":    apiUserData.Image,
				"nickname": apiUserData.Nickname,
				"name":     apiUserData.Name,
				"score":    apiUserData.Score,
			}).Err()

			if err != nil {
				log.Printf("Error saving user data to Redis for sub %s: %v", sub, err)
				http.Error(w, "Failed to save user data", http.StatusInternalServerError)
				return
			}

			// Update the userData variable with fetched data
			userData = apiUserData
		}

		// Return user data
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(userData)
	})))

	http.Handle("/users", corsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys, err := client.Keys(context.Background(), "user:*").Result()
		if err != nil {
			log.Printf("Error retrieving keys from Redis: %v", err)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}

		users := make([]UserData, 0)
		for _, key := range keys {
			sub := strings.TrimPrefix(key, "user:")
			userData, err := getUserDataFromRedis(sub)
			if err != nil {
				log.Printf("Error getting user data from Redis for sub %s: %v", sub, err)
				continue
			}
			users = append(users, userData)
		}

		// Write users slice as JSON response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(users)
	})))

	http.Handle("/top-scores", corsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		keys, err := client.Keys(ctx, "user:*").Result()
		if err != nil {
			log.Printf("Error retrieving keys from Redis: %v", err)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}

		type UserScore struct {
			Sub      string `json:"sub"`
			Score    int    `json:"score"`
			Nickname string `json:"nickname"`
			Image    string `json:"image"`
		}

		var topScores []UserScore

		for _, key := range keys {
			sub := strings.TrimPrefix(key, "user:")
			userData, err := getUserDataFromRedis(sub)
			if err != nil {
				log.Printf("Error getting user data from Redis for sub %s: %v", sub, err)
				continue
			}
			userScore := UserScore{
				Sub:      sub,
				Score:    userData.Score,
				Nickname: userData.Nickname,
				Image:    userData.Image,
			}
			topScores = append(topScores, userScore)
		}

		// Sort users by score in descending order
		sort.Slice(topScores, func(i, j int) bool {
			return topScores[i].Score > topScores[j].Score
		})

		// Get top 10 users
		if len(topScores) > 10 {
			topScores = topScores[:10]
		}

		// Write topScores as JSON response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(topScores)
	})))

	http.Handle("/user/incr", corsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse sub parameter from request URL
		sub := r.URL.Query().Get("sub")
		if sub == "" {
			http.Error(w, "Sub parameter is required", http.StatusBadRequest)
			return
		}

		// Increment the score in Redis
		redisKey := fmt.Sprintf("user:%s", sub)
		newScore, err := client.HIncrBy(context.Background(), redisKey, "score", 1).Result()
		if err != nil {
			log.Printf("Error incrementing score for user with sub %s in Redis: %v", sub, err)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}
		log.Printf("Score incremented for user with sub %s in Redis", sub)

		// Fetch updated user data from Redis
		userData, err := getUserDataFromRedis(sub)
		if err != nil {
			log.Printf("Error fetching updated user data from Redis for sub %s: %v", sub, err)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}

		// Send the updated score in the response
		response := struct {
			NewScore int      `json:"newScore"`
			UserData UserData `json:"userData"`
		}{
			NewScore: int(newScore),
			UserData: userData,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})))

	log.Printf("Server is listening at http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
