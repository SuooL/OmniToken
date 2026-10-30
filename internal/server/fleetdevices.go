package server

import "github.com/suool/omnitoken/internal/store"

// fleetDevices omits revoked principals explicitly consolidated by an admin.
// The registry remains intact for authentication, receipts and audit. A name
// match alone never removes a device from the fleet.
func (s *Server) fleetDevices() ([]store.DeviceRecord, error) {
	records, err := s.store.ListDevices()
	if err != nil {
		return nil, err
	}
	history, err := s.store.DeviceMergeHistory()
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	for _, merge := range history {
		for source, target := range aliases {
			if target == merge.From {
				aliases[source] = merge.To
			}
		}
		aliases[merge.From] = merge.To
	}
	present := map[string]bool{}
	for _, record := range records {
		present[record.DeviceID] = true
	}
	out := make([]store.DeviceRecord, 0, len(records))
	for _, record := range records {
		if target := aliases[record.DeviceID]; record.RevokedAt > 0 && target != "" && target != record.DeviceID && present[target] {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}
