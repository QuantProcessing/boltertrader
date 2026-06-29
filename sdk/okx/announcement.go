package okx

import (
	"context"
	"net/http"
)

// Announcement represents a single announcement detail from OKX API
// Based on /api/v5/support/announcements response structure
type Announcement struct {
	Title        string `json:"title"`
	URL          string `json:"url"`           // Full URL to announcement
	AnnType      string `json:"annType"`       // Type of announcement
	PublishTime  string `json:"pTime"`         // Timestamp in milliseconds
	BusinessTime string `json:"businessPTime"` // Business timestamp in milliseconds
}

// announcementDataWrapper matches the structure inside "data" array
type announcementDataWrapper struct {
	Details   []Announcement `json:"details"`
	TotalPage string         `json:"totalPage"`
}

// GetAnnouncements returns a list of announcements.
// annType: Announcement type (optional)
// page: Page number (optional, default 1)
func (c *Client) GetAnnouncements(ctx context.Context, annType string) ([]Announcement, error) {
	path := "/api/v5/support/announcements"
	if annType != "" {
		path += "?annType=" + annType
	}

	wrappers, err := Request[announcementDataWrapper](c, ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, err
	}

	var allAnns []Announcement
	for _, w := range wrappers {
		allAnns = append(allAnns, w.Details...)
	}

	return allAnns, nil
}
