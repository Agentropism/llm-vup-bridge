package model

type UnifiedEvent struct {
	Platform    string `json:"platform"` // "qq" | "bilibili" | #"telegram(可能会有，暂时不兼容)"#
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	GroupID     string `json:"group_id"`
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // "text" | "gift" | "super_chat"
}
