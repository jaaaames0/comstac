package ui

import (
	"fmt"
	"net/http"
	"strings"

	"comstac/internal/relay"
)

func outboundForm(req *http.Request, from string) (relay.OutboundMessage, error) {
	msg := relay.OutboundMessage{Subject: strings.TrimSpace(req.FormValue("subject")), BodyText: req.FormValue("body")}
	for _, f := range []struct {
		name string
		dst  *[]string
	}{{"to", &msg.To}, {"cc", &msg.CC}, {"bcc", &msg.BCC}} {
		addresses, err := relay.ParseRecipients(req.FormValue(f.name))
		if err != nil {
			return msg, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.dst = addresses
	}
	if len(msg.To) == 0 {
		return msg, fmt.Errorf("a To recipient is required")
	}
	attachments, err := extractAttachments(req)
	if err != nil {
		return msg, err
	}
	msg.Attachments = attachments
	return msg, relay.ValidateMessage(from, msg)
}

func renderDraftError(w http.ResponseWriter, req *http.Request, replyID int64, err error) {
	message := err.Error()
	if req.MultipartForm != nil && len(req.MultipartForm.File["attachments"]) > 0 {
		message += " Please reselect attachments before sending."
	}
	if req.URL.Path == "/ui/reply" {
		execute(w, "reply", replyData{ReplyToID: replyID, To: req.FormValue("to"), CC: req.FormValue("cc"), BCC: req.FormValue("bcc"), Subject: req.FormValue("subject"), QuotedBody: req.FormValue("body"), Error: message})
	} else {
		execute(w, "compose", composeData{To: req.FormValue("to"), CC: req.FormValue("cc"), BCC: req.FormValue("bcc"), Subject: req.FormValue("subject"), Body: req.FormValue("body"), Error: message})
	}
}
