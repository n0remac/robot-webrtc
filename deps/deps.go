package deps

import (
	"robot-webrtc/db"

	"gorm.io/gorm"
)

type Deps struct {
	DB   *gorm.DB
	Docs *db.DocumentStore
}
