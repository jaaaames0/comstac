package ui

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"comstac/internal/relay"
	"comstac/internal/store"
)

func sendAndRecord(ctx context.Context, db *sql.DB, sender mailSender, msg relay.OutboundMessage, replyToID int64) (sendErr, historyErr error) {
	msgID, raw, sendErr := sender.Send(ctx, msg)
	if sendErr != nil {
		slog.Warn("outbound send outcome", "component", "ui", "err", sendErr)
	}
	if db != nil && len(raw) > 0 {
		// SMTP has finished; preserving its outcome must survive a disconnected
		// HTTP request. Keep the detached write deliberately short.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, historyErr = store.SaveOutbound(saveCtx, db, sender.From(), strings.Join(msg.To, ", "), msg.Subject, msg.BodyText, msgID, replyToID, raw, sendErr)
		if historyErr != nil {
			slog.Error("save outbound outcome", "component", "ui", "err", historyErr)
		}
	}
	return sendErr, historyErr
}

func renderSendResult(w http.ResponseWriter, msg relay.OutboundMessage, sendErr, historyErr error) {
	if errors.Is(sendErr, relay.ErrDeliveryUnknown) {
		execute(w, "send_unknown", sendErrorData{Msg: sendErr.Error()})
		return
	}
	if sendErr != nil {
		execute(w, "send_error", sendErrorData{Msg: sendErr.Error()})
		return
	}
	warning := ""
	if historyErr != nil {
		warning = "The relay accepted this message, but saving sent history failed. Do not resend this accepted message."
	}
	execute(w, "send_success", sendSuccessData{To: strings.Join(msg.To, ", "), Subject: msg.Subject, Warning: warning})
}
