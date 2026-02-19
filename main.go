package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"ai-saas-dashboard/handlers"
	"ai-saas-dashboard/middleware"
	"ai-saas-dashboard/models"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var db *sql.DB

func main() {
	// Load environment variables
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgresql://saas_user:saas_password@localhost:5432/saas_db?sslmode=disable"
	}

	claudeAPIKey := os.Getenv("CLAUDE_API_KEY")
	if claudeAPIKey == "" {
		log.Fatal("CLAUDE_API_KEY environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET environment variable is required")
	}

	// Initialize database
	var err error
	db, err = sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}
	defer db.Close()

	// Test database connection
	err = db.Ping()
	if err != nil {
		log.Fatalf("Error pinging database: %v", err)
	}

	// Run migrations
	err = models.RunMigrations(db)
	if err != nil {
		log.Fatalf("Error running migrations: %v", err)
	}

	log.Println("Database connected and migrations completed successfully")

	// Initialize router
	r := chi.NewRouter()

	// Middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	// CORS configuration
	corsOrigins := os.Getenv("CORS_ORIGINS")
	if corsOrigins == "" {
		corsOrigins = "http://localhost:5173"
	}

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{corsOrigins, "http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Initialize handlers
	authHandler := handlers.NewAuthHandler(db, jwtSecret)
	dashboardHandler := handlers.NewDashboardHandler(db)
	chatHandler := handlers.NewChatHandler(db, claudeAPIKey)

	// Public routes
	r.Route("/api/auth", func(r chi.Router) {
		r.Use(middleware.RateLimiter(5, time.Minute)) // 5 requests per minute
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)
	})

	// Protected routes
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(jwtSecret))

		// Dashboard routes
		r.Route("/dashboard", func(r chi.Router) {
			r.Use(middleware.RateLimiter(60, time.Minute)) // 60 requests per minute
			r.Get("/metrics", dashboardHandler.GetMetrics)
			r.Get("/charts", dashboardHandler.GetChartData)
		})

		// Chat routes
		r.Route("/chat", func(r chi.Router) {
			r.Use(middleware.RateLimiter(20, time.Minute)) // 20 requests per minute
			r.Post("/", chatHandler.SendMessage)
			r.Get("/history", chatHandler.GetHistory)
			r.Get("/conversations", chatHandler.GetConversations)
		})
	})

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Start server
	addr := fmt.Sprintf(":%s", port)
	log.Printf("Server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
