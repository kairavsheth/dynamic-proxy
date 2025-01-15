package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
)

type ProxyMapping struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

var firestoreClient *firestore.Client
var ctx = context.Background()

var adminUsername = os.Getenv("ADMIN_USERNAME")
var adminPassword = os.Getenv("ADMIN_PASSWORD")

func main() {
	// Initialize Firestore
	var err error
	firestoreClient, err = firestore.NewClient(ctx, os.Getenv("PROJECT_ID"))
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer firestoreClient.Close()

	router := gin.Default()

	// Serve static files for admin panel
	router.Static("/static", "./static")
	router.LoadHTMLGlob("templates/*")

	// Admin routes with basic authentication middleware
	admin := router.Group("/admin", basicAuthMiddleware)
	{
		admin.GET("/", adminPage)
		admin.GET("/mappings", listMappingsAPI)
		admin.POST("/mappings", createMappingAPI)
		admin.PUT("/mappings/:id", updateMappingAPI)
		admin.DELETE("/mappings/:id", deleteMappingAPI)
	}

	// Proxy route
	router.Any("/:id/*path", proxyRequest)

	// Start server
	port := "8080"
	log.Printf("Server running on port %s", port)
	router.Run(":" + port)
}

// Basic Authentication Middleware
func basicAuthMiddleware(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.Header("WWW-Authenticate", `Basic realm="Restricted"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		c.Abort()
		return
	}

	// Decode the Base64-encoded credentials
	payload := strings.TrimPrefix(authHeader, "Basic ")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authentication"})
		c.Abort()
		return
	}

	// Check if the username and password are correct
	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 || credentials[0] != adminUsername || credentials[1] != adminPassword {
		c.Header("WWW-Authenticate", `Basic realm="Restricted"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		c.Abort()
		return
	}

	// Proceed to the next handler
	c.Next()
}

// Other functions remain unchanged...

// Admin page handler
func adminPage(c *gin.Context) {
	c.HTML(http.StatusOK, "admin.html", nil)
}

// List mappings for the API
func listMappingsAPI(c *gin.Context) {
	mappings := []ProxyMapping{}
	iter := firestoreClient.Collection("proxy_mappings").Documents(ctx)
	for {
		doc, err := iter.Next()
		if err != nil {
			break
		}
		var mapping ProxyMapping
		doc.DataTo(&mapping)
		mappings = append(mappings, mapping)
	}
	c.JSON(http.StatusOK, mappings)
}

// Create a mapping through the API
func createMappingAPI(c *gin.Context) {
	var mapping ProxyMapping
	if err := c.ShouldBindJSON(&mapping); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := firestoreClient.Collection("proxy_mappings").Doc(mapping.ID).Set(ctx, mapping)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create mapping"})
		return
	}
	c.JSON(http.StatusOK, mapping)
}

// Update a mapping through the API
func updateMappingAPI(c *gin.Context) {
	id := c.Param("id")
	var mapping ProxyMapping
	if err := c.ShouldBindJSON(&mapping); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := firestoreClient.Collection("proxy_mappings").Doc(id).Set(ctx, mapping)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update mapping"})
		return
	}
	c.JSON(http.StatusOK, mapping)
}

// Delete a mapping through the API
func deleteMappingAPI(c *gin.Context) {
	id := c.Param("id")
	_, err := firestoreClient.Collection("proxy_mappings").Doc(id).Delete(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete mapping"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// Proxy request
func proxyRequest(c *gin.Context) {
	id := c.Param("id")
	path := c.Param("path")

	// Retrieve the URL mapping from Firestore
	doc, err := firestoreClient.Collection("proxy_mappings").Doc(id).Get(ctx)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Mapping not found"})
		return
	}
	var mapping ProxyMapping
	doc.DataTo(&mapping)

	// Forward the request
	backendURL := fmt.Sprintf("%s%s", mapping.URL, path)
	req, err := http.NewRequest(c.Request.Method, backendURL, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}

	// Copy headers
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to forward request"})
		return
	}
	defer resp.Body.Close()

	// Copy response headers and body
	for key, values := range resp.Header {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)

	// Copy response body
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		log.Printf("Failed to write response body: %v", err)
	}
}
