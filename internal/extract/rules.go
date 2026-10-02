package extract

import (
	"regexp"
	"strings"
	"time"
)

// Trigger words decide whether a dated sentence is worth suggesting and of
// what kind. A date with no trigger nearby is ignored; precision matters
// more than recall because every suggestion asks for the user's attention.
var (
	reExpiry   = regexp.MustCompile(`(?i)\b(expir(e|es|ed|y|ing|ation)|valid (until|till|thru|through))\b`)
	rePromoA   = regexp.MustCompile(`(?i)(\b(sale|offers?|discounts?|promo(tion)?s?|deals?|vouchers?|coupons?)\b|\d+\s?% off)`)
	rePromoB   = regexp.MustCompile(`(?i)\b(ends?|ending|until|till|through|thru|expires?|last (day|chance)|now through)\b`)
	reDeadline = regexp.MustCompile(`(?i)\b(due|deadline|remov(e|ed|al)|cancel(l?ation|led|s)?|ends|ending|ended|by the end of|closes|closing|last day|renew(s|al)?|overdue|lapses?|suspend(ed|s)?|complete .{0,40}before|submit .{0,40}by|pay .{0,40}by)\b`)
	// Free-text sentences need booking-type wording; short labels in
	// "Label: value" layouts may use broader words ("Return Date Time").
	reEvent      = regexp.MustCompile(`(?i)\b(pick[- ]?up|drop[- ]?off|check[- ]?in|check[- ]?out|appointment|interview|reservation|booked for|scheduled for|delivery|collection)\b`)
	reEventLabel = regexp.MustCompile(`(?i)\b(return|depart(s|ure)?|arriv(e|es|al)|start|end|begins?|date|time|meeting)\b`)
	reDueTo      = regexp.MustCompile(`(?i)\bdue to\b`)
	reSentence   = regexp.MustCompile(`[.!?](\s+|$)`)
	reTitleCut   = regexp.MustCompile(`(?i)^(re|fwd?|fw):\s*`)
	reSymbols    = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}]`)
)

// classify returns the candidate kind for a sentence (or, with label set,
// a short field label), or "".
func classify(s string, label bool) string {
	s = reDueTo.ReplaceAllString(s, "")
	switch {
	case reExpiry.MatchString(s):
		return "expiry"
	case rePromoA.MatchString(s) && rePromoB.MatchString(s):
		return "promo"
	case reDeadline.MatchString(s):
		return "deadline"
	case reEvent.MatchString(s), label && reEventLabel.MatchString(s):
		return "event"
	}
	return ""
}

// sentences splits a line at sentence ends, keeping dates like "3 Oct.
// 2026" intact well enough for trigger matching.
func sentences(line string) []string {
	var out []string
	last := 0
	for _, m := range reSentence.FindAllStringIndex(line, -1) {
		out = append(out, strings.TrimSpace(line[last:m[1]]))
		last = m[1]
	}
	if rest := strings.TrimSpace(line[last:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

func cleanSubject(s string) string {
	s = normalize(reSymbols.ReplaceAllString(s, ""))
	for reTitleCut.MatchString(s) {
		s = reTitleCut.ReplaceAllString(s, "")
	}
	return s
}

// fromRules applies the generic trigger rules to every dated sentence.
func fromRules(d *doc, in Input) []Candidate {
	home := loadZone(HomeZone)
	anchorDay := in.Date.In(home)
	anchorDay = time.Date(anchorDay.Year(), anchorDay.Month(), anchorDay.Day(), 0, 0, 0, 0, home)
	subject := cleanSubject(in.Subject)
	subjectKind := classify(subject, false)
	sender := senderName(in.From)
	var out []Candidate

	add := func(kind, label, sentence string, day time.Time, hasTime bool, h, m int) {
		// Events must lie after the message day (a "booking date" equal to
		// the send date is history); deadlines may fall on it.
		if kind == "event" && !day.After(anchorDay) || day.Before(anchorDay) {
			return
		}
		c := Candidate{Extractor: "rules", Kind: kind, TZ: HomeZone, Evidence: truncate(sentence, 240)}
		if hasTime {
			c.StartLocal = localDateTime(time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, home))
		} else {
			c.AllDay = true
			c.StartLocal = localDate(day)
		}
		switch {
		case label != "":
			c.Title = truncate(label, 40) + " · " + sender
		case subjectKind == kind && subject != "":
			c.Title = truncate(subject, 80)
		default:
			c.Title = truncate(sentence, 80)
		}
		c.Notes = c.Evidence
		c.Key = kind + "/" + senderDomain(in.From) + "/" + c.StartLocal
		out = append(out, c)
	}

	prev := ""
	for _, line := range d.Lines {
		for _, sent := range sentences(line) {
			kind := classify(sent, false)
			label := ""
			// Label/value layouts: "Pick-up time: | Sat 02 May 2026 at 06:40".
			if before, _, ok := strings.Cut(sent, "|"); ok && len(before) <= 40 {
				if k := classify(before, true); k != "" {
					kind, label = k, strings.TrimRight(strings.TrimSpace(before), ":")
				}
			}
			if kind == "" && len(prev) <= 40 {
				if k := classify(prev, true); k != "" {
					kind, label = k, strings.TrimRight(prev, ":")
				}
			}
			if kind == "" {
				continue
			}
			dates := findDates(sent)
			for _, dm := range dates {
				if day, ok := dm.resolve(in.Date, home); ok {
					add(kind, label, sent, day, dm.HasTime, dm.Hour, dm.Min)
				}
			}
			if len(dates) == 0 && kind != "event" {
				if day, ok := relativeDate(sent, in.Date, home); ok {
					add(kind, label, sent, day, false, 0, 0)
				}
			}
		}
		prev = line
	}
	return out
}
