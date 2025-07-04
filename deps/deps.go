package deps

import (
	"github.com/n0remac/robot-webrtc/db"

	"gorm.io/gorm"
)

type Deps struct {
	DB   *gorm.DB
	Docs *db.DocumentStore
}
