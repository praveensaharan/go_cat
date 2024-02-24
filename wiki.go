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

	"github.com/gin-gonic/gin"
	// "github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var client *redis.Client

func init() {
	// err := godotenv.Load()
	// if err != nil {
	// 	log.Fatalf("Error loading .env file: %v", err)
	// }
	redisHostname := os.Getenv("REDIS_HOSTNAME")
	redisPort := os.Getenv("REDIS_PORT")
	redisPassword := os.Getenv("REDIS_PASSWORD")

	client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", redisHostname, redisPort),
		Password: redisPassword,
	})

	// Ping Redis to check the connection
	ctx := context.Background()
	pong, err := client.Ping(ctx).Result()
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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	router := gin.Default()

	router.Use(corsMiddleware())

	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, the server is running on port "+port)
	})

	router.GET("/user/:sub", getUserData)
	router.GET("/users", getUsers)
	router.GET("/top-scores", getTopScores)
	router.GET("/user/incr", incrementScore)

	if err := router.Run(":" + port); err != nil {
		log.Fatalf("Failed to start the server: %v", err)
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusOK)
			return
		}
		c.Next()
	}
}

func getUserData(c *gin.Context) {
	sub := c.Param("sub")
	if sub == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sub parameter is required"})
		return
	}

	userData, err := getUserDataFromRedis(sub)
	if err != nil {
		log.Printf("Error getting user data from Redis for sub %s: %v", sub, err)
		response, err := fetchUserDataFromAPI(sub)
		if err != nil {
			log.Printf("Error fetching user data from API for sub %s: %v", sub, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user data"})
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save user data"})
			return
		}

		// Update the userData variable with fetched data
		userData = apiUserData
	}

	// Return user data
	c.JSON(http.StatusOK, userData)
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

func storeUserDataInRedis(userData UserData) error {
	ctx := context.Background() // Create a background context
	redisKey := fmt.Sprintf("user:%s", userData.Sub)
	_, err := client.HMSet(ctx, redisKey, map[string]interface{}{
		"sub":      userData.Sub,
		"image":    userData.Image,
		"nickname": userData.Nickname,
		"name":     userData.Name,
		"score":    userData.Score,
	}).Result()
	if err != nil {
		return err
	}
	return nil
}

func getUsers(c *gin.Context) {
	keys, err := client.Keys(context.Background(), "user:*").Result()
	if err != nil {
		log.Printf("Error retrieving keys from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server error"})
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

	c.JSON(http.StatusOK, users)
}

func getTopScores(c *gin.Context) {
	ctx := context.Background()
	keys, err := client.Keys(ctx, "user:*").Result()
	if err != nil {
		log.Printf("Error retrieving keys from Redis: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server error"})
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

	c.JSON(http.StatusOK, topScores)
}

func incrementScore(c *gin.Context) {
	// Parse sub parameter from request URL
	sub := c.Query("sub")
	if sub == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sub parameter is required"})
		return
	}

	// Increment the score in Redis
	redisKey := fmt.Sprintf("user:%s", sub)
	newScore, err := client.HIncrBy(context.Background(), redisKey, "score", 1).Result()
	if err != nil {
		log.Printf("Error incrementing score for user with sub %s in Redis: %v", sub, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server error"})
		return
	}
	log.Printf("Score incremented for user with sub %s in Redis", sub)

	// Fetch updated user data from Redis
	userData, err := getUserDataFromRedis(sub)
	if err != nil {
		log.Printf("Error fetching updated user data from Redis for sub %s: %v", sub, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server error"})
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
	c.JSON(http.StatusOK, response)
}
