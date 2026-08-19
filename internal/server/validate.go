package server

import (
	"fmt"
	"net/mail"
	"strings"
)

// validateFrom checks that from, if non-empty, is a syntactically valid
// address whose domain is in allowedDomains. An empty from is always
// accepted: the mailer falls back to its configured SMTP user, which is not
// caller-controlled and needs no check. A nil/empty allowedDomains means no
// domain restriction is configured, so any syntactically valid from address
// is accepted.
func validateFrom(from string, allowedDomains []string) error {
	if from == "" {
		return nil
	}

	addr, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}

	if len(allowedDomains) == 0 {
		// No restriction configured: any syntactically valid address is accepted.
		return nil
	}

	domain := domainOf(addr.Address)
	for _, allowed := range allowedDomains {
		if strings.EqualFold(domain, allowed) {
			return nil
		}
	}
	return fmt.Errorf("from address domain %q is not allowed", domain)
}

// validateRecipients checks that to is non-empty and that every address in
// to, cc, and bcc is syntactically valid.
func validateRecipients(to, cc, bcc []string) error {
	if len(to) == 0 {
		return fmt.Errorf("at least one recipient is required")
	}
	for _, addr := range to {
		if _, err := mail.ParseAddress(addr); err != nil {
			return fmt.Errorf("invalid to address %q: %w", addr, err)
		}
	}
	for _, addr := range cc {
		if _, err := mail.ParseAddress(addr); err != nil {
			return fmt.Errorf("invalid cc address %q: %w", addr, err)
		}
	}
	for _, addr := range bcc {
		if _, err := mail.ParseAddress(addr); err != nil {
			return fmt.Errorf("invalid bcc address %q: %w", addr, err)
		}
	}
	return nil
}

// domainOf returns the domain portion of an email address, or "" if there is
// no "@" in it.
func domainOf(address string) string {
	i := strings.LastIndex(address, "@")
	if i < 0 {
		return ""
	}
	return address[i+1:]
}
