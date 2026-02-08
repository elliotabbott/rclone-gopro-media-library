package api

import "time"

// searchResult represents the top-level structure of the JSON document.
type searchResult struct {
	Pages    *pages          `json:"_pages"`
	Embedded *searchEmbedded `json:"_embedded"`
}

// pages represents the "_pages" object in the JSON.
type pages struct {
	CurrentPage *int `json:"current_page"`
	PerPage     *int `json:"per_page"`
	TotalItems  *int `json:"total_items"`
	TotalPages  *int `json:"total_pages"`
}

// searchEmbedded represents the "_embedded" object in the JSON.
type searchEmbedded struct {
	Errors []interface{} `json:"errors"` // Using interface{} as errors array is empty, type is unknown, and nil slice handles absence.
	Media  []Media       `json:"media"`
}

// Media represents an item in the "Media" array.
type Media struct {
	CameraModel        *string    `json:"camera_model"` // Can be null
	CapturedAt         *time.Time `json:"captured_at"`
	ContentTitle       *string    `json:"content_title"` // Can be null
	ContentType        *string    `json:"content_type"`  // Can be null
	CreatedAt          *time.Time `json:"created_at"`
	GoproUserID        *string    `json:"gopro_user_id"`
	GoproMedia         *bool      `json:"gopro_media"`
	Filename           *string    `json:"filename"` // Can be null
	FileExtension      *string    `json:"file_extension"`
	FileSize           int64      `json:"file_size"` // Can be zero from API
	Height             *int       `json:"height"`
	Fov                *string    `json:"fov"` // Can be null
	ID                 *string    `json:"id"`
	ItemCount          *int       `json:"item_count"`
	MceType            *string    `json:"mce_type"` // Can be null
	MomentsCount       *int       `json:"moments_count"`
	OnPublicProfile    *bool      `json:"on_public_profile"`
	Orientation        *int       `json:"orientation"`
	PlayAs             *string    `json:"play_as"`
	ReadyToEdit        *bool      `json:"ready_to_edit"`
	ReadyToView        *string    `json:"ready_to_view"`
	Resolution         *string    `json:"resolution"` // Can be null
	SourceDuration     *string    `json:"source_duration"`
	Token              *string    `json:"token"`
	Type               *string    `json:"type"`
	Width              *int       `json:"width"`
	Stabilized         *bool      `json:"stabilized"`
	SubmittedAt        *string    `json:"submitted_at"` // Can be null
	ThumbnailAvailable *bool      `json:"thumbnail_available"`
	CapturedAtTimezone *string    `json:"captured_at_timezone"` // Can be null
	AvailableLabels    []string   `json:"available_labels"`
}
