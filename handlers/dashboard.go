package handlers

import (
	"ai-saas-dashboard/models"
	"database/sql"
	"encoding/json"
	"math/rand"
	"net/http"
	"time"
)

type DashboardHandler struct {
	db *sql.DB
}

func NewDashboardHandler(db *sql.DB) *DashboardHandler {
	return &DashboardHandler{db: db}
}

func (h *DashboardHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// In a real application, you would fetch these from your database
	// For demo purposes, we'll generate realistic mock data
	metrics := models.DashboardMetrics{
		TotalUsers:  1250 + rand.Intn(100),
		Revenue:     45678.50 + float64(rand.Intn(10000)),
		Growth:      12.5 + float64(rand.Intn(10)),
		ActiveUsers: 890 + rand.Intn(50),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (h *DashboardHandler) GetChartData(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Get date range from query params (default to 7 days)
	rangeParam := r.URL.Query().Get("range")
	days := 7
	switch rangeParam {
	case "30d":
		days = 30
	case "90d":
		days = 90
	case "1y":
		days = 365
	default:
		days = 7
	}

	// Generate mock chart data
	chartData := models.ChartData{
		Revenue:    generateChartData(days, 1000, 5000),
		Users:      generateChartData(days, 50, 200),
		Engagement: generateChartData(days, 60, 100),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chartData)
}

func generateChartData(days int, minValue, maxValue float64) []models.ChartDataPoint {
	data := make([]models.ChartDataPoint, days)
	now := time.Now()
	baseValue := minValue + (maxValue-minValue)/2

	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -days+i+1)
		
		// Add some realistic variation
		variation := (rand.Float64() - 0.5) * (maxValue - minValue) * 0.3
		trend := float64(i) * (maxValue - minValue) / float64(days) * 0.5
		value := baseValue + variation + trend

		// Ensure value is within bounds
		if value < minValue {
			value = minValue
		}
		if value > maxValue {
			value = maxValue
		}

		data[i] = models.ChartDataPoint{
			Date:  date.Format("2006-01-02"),
			Value: value,
		}
	}

	return data
}
