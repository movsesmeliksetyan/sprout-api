// Package uploads hands out presigned upload URLs and checks what was
// uploaded before another feature uses it.
package uploads

import "slices"

// Purpose is what an upload will be used for. It decides which content
// types and sizes are accepted.
type Purpose string

// The purposes of the contract §2.2.
const (
	PurposeAvatar    Purpose = "avatar"
	PurposeGoalImage Purpose = "goal_image"
	PurposeReceipt   Purpose = "receipt"
	PurposeStatement Purpose = "statement"
)

const megabyte = 1 << 20

// policy is one row of the table in contract §2.2.
type policy struct {
	contentTypes []string
	maxBytes     int64
}

var images = []string{"image/jpeg", "image/png", "image/heic"}

var policies = map[Purpose]policy{
	PurposeAvatar:    {contentTypes: images, maxBytes: 5 * megabyte},
	PurposeGoalImage: {contentTypes: images, maxBytes: 5 * megabyte},
	PurposeReceipt:   {contentTypes: images, maxBytes: 10 * megabyte},
	PurposeStatement: {
		contentTypes: []string{
			"text/csv",
			"application/x-ofx",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/pdf",
			"application/octet-stream",
		},
		maxBytes: 15 * megabyte,
	},
}

func (p policy) allows(contentType string) bool {
	return slices.Contains(p.contentTypes, contentType)
}
