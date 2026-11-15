// Package installation models a Portico installation and reads the
// historical records the Swift app wrote (installation-v5.json back to
// portal-v1.json). It never writes to disk.
package installation

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/chrisbanes/portico/helper/internal/portal"
)

type Logging string

const (
	LoggingUndecided Logging = "undecided"
	LoggingEnabled   Logging = "enabled"
	LoggingDisabled  Logging = "disabled"
)

type LaunchAtLoginOffer string

const (
	OfferNotOffered LaunchAtLoginOffer = "notOffered"
	OfferPresented  LaunchAtLoginOffer = "presented"
	OfferDeclined   LaunchAtLoginOffer = "declined"
	OfferAccepted   LaunchAtLoginOffer = "accepted"
)

type Lifecycle string

const (
	LifecycleActive                  Lifecycle = "active"
	LifecyclePendingTailnetRejection Lifecycle = "pendingTailnetRejection"
	LifecyclePendingRemoval          Lifecycle = "pendingRemoval"
)

const AlertCrossTailnetRejection = "crossTailnetRejection"

type Installation struct {
	Binding            *TailnetBinding
	Portals            []Portal
	Alerts             []Alert
	Logging            Logging
	LaunchAtLoginOffer LaunchAtLoginOffer
}

type TailnetBinding struct {
	Name           string
	MagicDNSSuffix string
}

type Portal struct {
	// ID is a lowercase UUID, matching the runtime's identity directory names.
	ID           string
	Name         string
	Destination  portal.Destination
	PublicAccess portal.PublicAccessMode
	CreatedAt    time.Time
	DesiredState portal.DesiredState
	Lifecycle    Lifecycle
	// RemovalAssignedName is only set on a pendingRemoval Portal.
	RemovalAssignedName *string
}

type Alert struct {
	ID                     string
	Kind                   string
	PortalName             string
	AssignedName           *string
	ExpectedMagicDNSSuffix *string
	RejectedMagicDNSSuffix *string
	CreatedAt              time.Time
}

// New returns the record of a fresh installation.
func New() Installation {
	return Installation{Logging: LoggingUndecided, LaunchAtLoginOffer: OfferNotOffered}
}

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

var errInvalid = errors.New("invalid installation")

// Validate ports the Swift InstallationRecord.validateCurrentRecord rules,
// plus the enum and destination checks Swift's decoder applied.
func Validate(in Installation) error {
	switch in.Logging {
	case LoggingUndecided, LoggingEnabled, LoggingDisabled:
	default:
		return errInvalid
	}
	switch in.LaunchAtLoginOffer {
	case OfferNotOffered, OfferPresented, OfferDeclined, OfferAccepted:
	default:
		return errInvalid
	}
	ids := map[string]bool{}
	for _, p := range in.Portals {
		// Config.Validate covers the UUID, the DNS-label name, the destination
		// and the Local-App-only rule for public access.
		config := portal.Config{ID: p.ID, Name: p.Name, Destination: p.Destination, PublicAccess: p.PublicAccess}
		if ids[p.ID] || !uuidPattern.MatchString(p.ID) || config.Validate() != nil {
			return errInvalid
		}
		ids[p.ID] = true
		if p.DesiredState != portal.DesiredStateEnabled && p.DesiredState != portal.DesiredStateStopped {
			return errInvalid
		}
		switch p.Lifecycle {
		case LifecycleActive:
		case LifecyclePendingTailnetRejection:
			if in.Binding == nil {
				return errInvalid
			}
		case LifecyclePendingRemoval:
		default:
			return errInvalid
		}
		if p.RemovalAssignedName != nil && (p.Lifecycle != LifecyclePendingRemoval || !dnsLabelPattern.MatchString(*p.RemovalAssignedName)) {
			return errInvalid
		}
	}
	if b := in.Binding; b != nil && (b.Name == "" || !isMagicDNSSuffix(b.MagicDNSSuffix)) {
		return errInvalid
	}
	alertIDs := map[string]bool{}
	for _, a := range in.Alerts {
		if alertIDs[a.ID] || !uuidPattern.MatchString(a.ID) || a.Kind != AlertCrossTailnetRejection ||
			!dnsLabelPattern.MatchString(a.PortalName) ||
			(a.AssignedName != nil && !dnsLabelPattern.MatchString(*a.AssignedName)) ||
			(a.ExpectedMagicDNSSuffix != nil && !isMagicDNSSuffix(*a.ExpectedMagicDNSSuffix)) ||
			(a.RejectedMagicDNSSuffix != nil && !isMagicDNSSuffix(*a.RejectedMagicDNSSuffix)) {
			return errInvalid
		}
		alertIDs[a.ID] = true
	}
	return nil
}

func isMagicDNSSuffix(suffix string) bool {
	if !strings.HasSuffix(suffix, ".ts.net") {
		return false
	}
	for _, label := range strings.Split(suffix, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
