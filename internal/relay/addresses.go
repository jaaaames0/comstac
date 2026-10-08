package relay

import (
	"fmt"
	"net/mail"
	"strings"
)

// ParseRecipients preserves display names and quoted commas. Envelope callers
// parse each canonical result and use Address, never the display-name string.
func ParseRecipients(raw string) ([]string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, fmt.Errorf("recipient list contains a line break")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	addresses, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient list: %w", err)
	}
	result := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if a.Name == "" {
			result = append(result, a.Address)
		} else {
			result = append(result, a.String())
		}
	}
	return result, nil
}

func normalizeRecipients(msg OutboundMessage) (OutboundMessage, []string, error) {
	var envelope []string
	for _, field := range []*[]string{&msg.To, &msg.CC, &msg.BCC} {
		addresses, err := ParseRecipients(strings.Join(*field, ", "))
		if err != nil {
			return msg, nil, err
		}
		*field = addresses
		for _, text := range addresses {
			a, err := mail.ParseAddress(text)
			if err != nil {
				return msg, nil, err
			}
			envelope = append(envelope, a.Address)
		}
	}
	if len(envelope) == 0 || len(envelope) > 100 {
		return msg, nil, fmt.Errorf("provide between 1 and 100 recipients")
	}
	return msg, envelope, nil
}
