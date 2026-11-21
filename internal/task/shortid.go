package task

import (
	"slices"
	"strings"
)

// ShortIDs returns copyable references for a complete catalog. Resolve also
// accepts names, so another task's name can make an otherwise unique ID
// prefix ambiguous. Only adjacent sorted IDs can share a longer prefix.
func ShortIDs(records []Record) map[ID]string {
	ids := make([]ID, 0, len(records))
	names := make(map[string]ID, len(records))
	for _, r := range records {
		ids = append(ids, r.Spec.ID)
		names[r.Spec.Name] = r.Spec.ID
	}
	slices.Sort(ids)
	refs := make(map[ID]string, len(ids))
	for i, id := range ids {
		ref := string(id)
		for n := 8; n < len(id); n++ {
			prefix := string(id[:n])
			if other, ok := names[prefix]; ok && other != id {
				continue
			}
			if i > 0 && strings.HasPrefix(string(ids[i-1]), prefix) {
				continue
			}
			if i+1 < len(ids) && strings.HasPrefix(string(ids[i+1]), prefix) {
				continue
			}
			ref = prefix
			break
		}
		refs[id] = ref
	}
	return refs
}
