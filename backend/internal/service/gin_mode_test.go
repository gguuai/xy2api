package service

import "github.com/gin-gonic/gin"

// Set the process-wide fixture mode before parallel tests start. Test bodies
// must not rewrite Gin globals while other cases create contexts or routers.
func init() { gin.SetMode(gin.TestMode) }
