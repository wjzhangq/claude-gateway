package handler

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
)

type ConfigHandler struct {
	cfgPath  string
	cfg      *config.Config
	reloadFn func() error
}

func NewConfigHandler(cfgPath string, cfg *config.Config, reloadFn func() error) *ConfigHandler {
	return &ConfigHandler{cfgPath: cfgPath, cfg: cfg, reloadFn: reloadFn}
}

type limitsResponse struct {
	BackendDailyMax    float64                `json:"backend_daily_max"`
	AWSDailyMax        float64                `json:"aws_daily_max"`
	AWSMonthlyMax      float64                `json:"aws_monthly_max"`
	UserDailyLimits    []userLimitResponse    `json:"user_daily_limits"`
	BackendDailyLimits []backendLimitResponse `json:"backend_daily_limits"`
}

type userLimitResponse struct {
	Itcode          string  `json:"itcode"`
	BackendDailyUSD float64 `json:"backend_daily_usd"`
	AWSDailyUSD     float64 `json:"aws_daily_usd"`
	AWSMonthlyUSD   float64 `json:"aws_monthly_usd"`
}

type backendLimitResponse struct {
	Name     string  `json:"name"`
	DailyUSD float64 `json:"daily_usd"`
}

func (h *ConfigHandler) GetLimits(c *gin.Context) {
	limits := make([]userLimitResponse, len(h.cfg.UserDailyLimits))
	for i, l := range h.cfg.UserDailyLimits {
		limits[i] = userLimitResponse{
			Itcode:          l.Itcode,
			BackendDailyUSD: l.BackendDailyUSD,
			AWSDailyUSD:     l.AWSDailyUSD,
			AWSMonthlyUSD:   l.AWSMonthlyUSD,
		}
	}
	backendLimits := make([]backendLimitResponse, len(h.cfg.BackendDailyLimits))
	for i, l := range h.cfg.BackendDailyLimits {
		backendLimits[i] = backendLimitResponse{
			Name:     l.Name,
			DailyUSD: l.DailyUSD,
		}
	}
	c.JSON(http.StatusOK, limitsResponse{
		BackendDailyMax:    h.cfg.BackendDailyMax,
		AWSDailyMax:        h.cfg.AWS.AWSDailyMax,
		AWSMonthlyMax:      h.cfg.AWS.AWSMonthlyMax,
		UserDailyLimits:    limits,
		BackendDailyLimits: backendLimits,
	})
}

type updateLimitsRequest struct {
	BackendDailyMax *float64           `json:"backend_daily_max"`
	AWSDailyMax     *float64           `json:"aws_daily_max"`
	AWSMonthlyMax   *float64           `json:"aws_monthly_max"`
	UserLimits      []userLimitRequest `json:"user_limits"`
}

type userLimitRequest struct {
	Itcode          string  `json:"itcode"`
	BackendDailyUSD float64 `json:"backend_daily_usd"`
	AWSDailyUSD     float64 `json:"aws_daily_usd"`
	AWSMonthlyUSD   float64 `json:"aws_monthly_usd"`
}

func (h *ConfigHandler) UpdateLimits(c *gin.Context) {
	var req updateLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data, err := os.ReadFile(h.cfgPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read config file failed"})
		return
	}
	content := string(data)

	if req.BackendDailyMax != nil {
		content = replaceYAMLValue(content, `backend_daily_max`, formatFloat(*req.BackendDailyMax))
	}

	if req.AWSDailyMax != nil {
		content = replaceYAMLValue(content, `aws_daily_max`, formatFloat(*req.AWSDailyMax))
	}

	if req.AWSMonthlyMax != nil {
		content = replaceYAMLValue(content, `aws_monthly_max`, formatFloat(*req.AWSMonthlyMax))
	}

	for _, ul := range req.UserLimits {
		content = replaceUserLimit(content, ul.Itcode, ul.BackendDailyUSD, ul.AWSDailyUSD, ul.AWSMonthlyUSD)
	}

	if err := os.WriteFile(h.cfgPath, []byte(content), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write config file failed"})
		return
	}

	if err := h.reloadFn(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("reload config failed: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func replaceYAMLValue(content, key, newValue string) string {
	re := regexp.MustCompile(`(?m)(^\s*` + regexp.QuoteMeta(key) + `:\s*)([^\s#]+)(.*)$`)
	return re.ReplaceAllString(content, "${1}"+newValue+"${3}")
}

func replaceUserLimit(content, itcode string, backendUSD, awsDailyUSD, awsMonthlyUSD float64) string {
	lines := strings.Split(content, "\n")
	var result []string
	found := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.Contains(line, `"`+itcode+`"`) || strings.Contains(line, `'`+itcode+`'`) || strings.Contains(line, itcode) {
			if strings.Contains(line, "itcode") {
				found = true
				result = append(result, line)
				for i+1 < len(lines) {
					next := lines[i+1]
					trimmed := strings.TrimSpace(next)
					if trimmed == "" || (!strings.HasPrefix(trimmed, "backend_daily_usd") && !strings.HasPrefix(trimmed, "aws_daily_usd") && !strings.HasPrefix(trimmed, "aws_monthly_usd") && !strings.HasPrefix(trimmed, "#")) {
						break
					}
					if strings.HasPrefix(trimmed, "backend_daily_usd") {
						next = replaceYAMLValue(next, "backend_daily_usd", formatFloat(backendUSD))
					} else if strings.HasPrefix(trimmed, "aws_daily_usd") {
						next = replaceYAMLValue(next, "aws_daily_usd", formatFloat(awsDailyUSD))
					} else if strings.HasPrefix(trimmed, "aws_monthly_usd") {
						next = replaceYAMLValue(next, "aws_monthly_usd", formatFloat(awsMonthlyUSD))
					}
					result = append(result, next)
					i++
				}
				continue
			}
		}
		result = append(result, line)
	}
	_ = found
	return strings.Join(result, "\n")
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}
