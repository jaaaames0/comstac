package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type OutboundListItem struct {
	ID        int64  `json:"id"`
	FromAddr  string `json:"from_addr"`
	ToAddr    string `json:"to_addr"`
	Subject   string `json:"subject"`
	Status    string `json:"status"`
	ErrorMsg  string `json:"error_msg,omitempty"`
	CreatedAt string `json:"created_at"`
}

func ListOutbound(ctx context.Context, db *sql.DB, limit int, beforeID int64) ([]OutboundListItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var sb strings.Builder
	sb.WriteString(`SELECT id, from_addr, to_addr, subject, status, COALESCE(error_msg,''), created_at FROM outbound_messages WHERE 1=1`)
	args := make([]any, 0, 2)
	if beforeID > 0 {
		sb.WriteString(` AND id < ?`)
		args = append(args, beforeID)
	}
	sb.WriteString(` ORDER BY id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list outbound: %w", err)
	}
	defer rows.Close()

	items := make([]OutboundListItem, 0, limit)
	for rows.Next() {
		var item OutboundListItem
		if err := rows.Scan(&item.ID, &item.FromAddr, &item.ToAddr, &item.Subject, &item.Status, &item.ErrorMsg, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan outbound row: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type OutboundRecord struct {
	ID         int64  `json:"id"`
	FromAddr   string `json:"from_addr"`
	ToAddr     string `json:"to_addr"`
	Subject    string `json:"subject"`
	MessageID  string `json:"message_id"`
	InReplyTo  int64  `json:"in_reply_to,omitempty"`
	Status     string `json:"status"`
	ErrorMsg   string `json:"error_msg,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func SaveOutbound(ctx context.Context, db *sql.DB, fromAddr, toAddr, subject, bodyText, messageID string, inReplyTo int64, rawMIME []byte, sendErr error) (*OutboundRecord, error) {
	status := "sent"
	errMsg := ""
	if sendErr != nil {
		status = "failed"
		errMsg = sendErr.Error()
	}

	var replyTo any
	if inReplyTo > 0 {
		replyTo = inReplyTo
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO outbound_messages
			(from_addr, to_addr, subject, body_text, message_id, in_reply_to, raw_mime, status, error_msg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, fromAddr, toAddr, subject, bodyText, messageID, replyTo, rawMIME, status, errMsg)
	if err != nil {
		return nil, fmt.Errorf("save outbound: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("outbound id: %w", err)
	}

	return &OutboundRecord{
		ID:        id,
		FromAddr:  fromAddr,
		ToAddr:    toAddr,
		Subject:   subject,
		MessageID: messageID,
		InReplyTo: inReplyTo,
		Status:    status,
		ErrorMsg:  errMsg,
	}, nil
}
