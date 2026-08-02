package server

import "github.com/suool/omnitoken/internal/store"

// deviceNames answers the one question every device-keyed view asks: what
// should this identity be called on screen.
//
// Two sources feed it, and the order between them is this type's whole content:
//
//   - `devices.display_name` — what a v2 device called itself when it enrolled.
//   - `device_labels` — a rename an operator typed into settings.
//
// The typed label wins. ADR-0015 settles 自报 over 旁观推断 for *attribution*,
// but neither side of that rule applies here: a label is not evidence about
// which machine produced an event, it is the operator's own word for a machine
// they own, and nothing a machine says about itself outranks that.
//
// An identity with neither is left alone rather than given a fallback, and that
// is the useful case rather than the empty one: a v1 identity already *is* its
// hostname, so there is nothing to add — while a v2 identity is a UUID, which is
// exactly why this exists. Without it the panel prints 36 characters of hex for
// every enrolled device.
//
// Resolution lives at the view boundary and not in the store on purpose: the
// events table is keyed by identity and has to stay that way. Joining a display
// name in during aggregation would make the grouping key depend on a setting
// anyone can edit, and renaming a device would silently reshape history.
type deviceNames map[string]string

// name returns what to print for identity, or "" when the identity is already
// the best name available.
func (n deviceNames) name(identity string) string { return n[identity] }

func (s *Server) deviceNames() (deviceNames, error) {
	registered, err := s.store.ListDevices()
	if err != nil {
		return nil, err
	}
	names := make(deviceNames, len(registered))
	for _, record := range registered {
		if record.DisplayName != "" {
			names[record.DeviceID] = record.DisplayName
		}
	}
	labels := map[string]string{}
	if err := s.store.GetSettingsJSON(store.DeviceLabelsKey, &labels); err != nil {
		return nil, err
	}
	for identity, label := range labels {
		if label != "" {
			names[identity] = label
		}
	}
	return names, nil
}

// namedBreakdownRow is a breakdown row plus the label the panel should print.
//
// Embedded rather than copied field by field so the wire shape only gains a
// key: every existing consumer keeps reading `key`, and the panel prefers
// `display_name` when it is there.
type namedBreakdownRow struct {
	store.BreakdownRow
	DisplayName string `json:"display_name,omitempty"`
}

func (s *Server) nameDeviceRows(rows []store.BreakdownRow) ([]namedBreakdownRow, error) {
	names, err := s.deviceNames()
	if err != nil {
		return nil, err
	}
	named := make([]namedBreakdownRow, len(rows))
	for i, row := range rows {
		named[i] = namedBreakdownRow{BreakdownRow: row, DisplayName: names.name(row.Key)}
	}
	return named, nil
}
