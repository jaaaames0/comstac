package validation

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/mail"
	"strings"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
)

// Result holds the annotation for a single inbound message.
// All fields use lowercase strings matching standard terminology:
// "pass", "fail", "softfail", "neutral", "none", "temperror", "permerror", "".
type Result struct {
	SPF        string `json:"spf,omitempty"`
	SPFDomain  string `json:"spf_domain,omitempty"`
	DKIM       string `json:"dkim,omitempty"`
	DKIMDomain string `json:"dkim_domain,omitempty"`
	DMARC      string `json:"dmarc,omitempty"` // the published policy: none/quarantine/reject
}

// ToJSON serialises the result. Returns empty string on error.
func (r Result) ToJSON() string {
	if r == (Result{}) {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// CheckMessage runs SPF, DKIM, and DMARC policy lookup for an inbound message.
// remoteIP may be nil (e.g. for IMAP-fetched messages) — SPF is skipped in that case.
func CheckMessage(rawMIME []byte, remoteIP net.IP, envelopeFrom string) Result {
	var res Result

	// --- DKIM ---
	verifications, err := dkim.Verify(bytes.NewReader(rawMIME))
	if err == nil && len(verifications) > 0 {
		// Use the first verification that passed; fall back to first result.
		best := verifications[0]
		for _, v := range verifications {
			if v.Err == nil {
				best = v
				break
			}
		}
		if best.Err == nil {
			res.DKIM = "pass"
		} else {
			res.DKIM = "fail"
		}
		res.DKIMDomain = best.Domain
	} else if err == nil && len(verifications) == 0 {
		res.DKIM = "none"
	}

	// --- SPF ---
	if remoteIP != nil && envelopeFrom != "" {
		fromDomain := domainOf(envelopeFrom)
		if fromDomain != "" {
			spfResult, _ := spf.CheckHost(remoteIP, fromDomain)
			res.SPF = strings.ToLower(string(spfResult))
			res.SPFDomain = fromDomain
		}
	}

	// --- DMARC policy lookup (from From: header) ---
	fromDomain := fromHeaderDomain(rawMIME)
	if fromDomain != "" {
		rec, err := dmarc.Lookup(fromDomain)
		if err == nil && rec != nil {
			res.DMARC = string(rec.Policy)
		} else if err == dmarc.ErrNoPolicy {
			res.DMARC = "none"
		} else if err != nil {
			slog.Warn("dmarc lookup", "component", "validation", "domain", fromDomain, "err", err)
		}
	}

	return res
}

func domainOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if parsed, err := mail.ParseAddress(addr); err == nil {
		addr = parsed.Address
	}
	if at := strings.LastIndex(addr, "@"); at >= 0 {
		return strings.ToLower(addr[at+1:])
	}
	return ""
}

func fromHeaderDomain(rawMIME []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMIME))
	if err != nil {
		return ""
	}
	return domainOf(msg.Header.Get("From"))
}
