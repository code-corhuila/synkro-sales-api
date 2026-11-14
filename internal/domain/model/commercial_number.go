package model

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	commercialPrefix = "VEN"
	// maxCommercialSequence is the largest value the six-digit format can carry.
	// Reaching it is an explicit failure, never a silent wrap or a longer number.
	maxCommercialSequence = 999999
	minCommercialYear     = 1000
	maxCommercialYear     = 9999
)

var (
	ErrInvalidCommercialNumber         = errors.New("commercial number is not VEN-YYYY-NNNNNN")
	ErrCommercialSequenceExhausted     = errors.New("commercial sequence for the year is exhausted")
	ErrCommercialNumberAlreadyAssigned = errors.New("sale already has a commercial number")

	commercialPattern = regexp.MustCompile(`^VEN-[1-9][0-9]{3}-(?:0[0-9]{5}|[1-9][0-9]{5})$`)

	// Colombia has no daylight saving time, so a fixed offset is exact and does
	// not depend on the host's tz database.
	colombia = time.FixedZone("COT", -5*60*60)
)

// CommercialYear is the calendar year, in Colombian time, that scopes the
// sequence of a sale created at the given instant.
func CommercialYear(at time.Time) int { return at.In(colombia).Year() }

// NewCommercialNumber formats the commercial reference of a sale. It is a
// commercial receipt reference, not a fiscal invoice number, and gaps are
// possible: only uniqueness and immutability are guaranteed.
func NewCommercialNumber(year int, sequence int64) (string, error) {
	if year < minCommercialYear || year > maxCommercialYear || sequence < 1 {
		return "", ErrInvalidCommercialNumber
	}
	if sequence > maxCommercialSequence {
		return "", ErrCommercialSequenceExhausted
	}
	return fmt.Sprintf("%s-%04d-%06d", commercialPrefix, year, sequence), nil
}

// ValidCommercialNumber reports whether value is a well-formed number with a
// sequence of at least one.
func ValidCommercialNumber(value string) bool {
	return commercialPattern.MatchString(value) && value[len(value)-6:] != "000000"
}

func (s *Sale) CommercialNumber() string { return s.commercialNumber }

// AssignCommercialNumber sets the number once. Replaying a stored sale keeps
// the number it already has.
func (s *Sale) AssignCommercialNumber(number string) error {
	if !ValidCommercialNumber(number) {
		return ErrInvalidCommercialNumber
	}
	if s.commercialNumber != "" {
		return ErrCommercialNumberAlreadyAssigned
	}
	s.commercialNumber = number
	return nil
}
