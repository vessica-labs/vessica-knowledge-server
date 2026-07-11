package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReplayEvents reconstructs versioned projections in an empty Store from the
// authoritative event stream. Events must be supplied in stream order.
func ReplayEvents(ctx context.Context, events []Event, destination Store) error {
	decode := func(e Event, key string, target any) error {
		raw, ok := e.Payload[key]
		if !ok {
			return fmt.Errorf("event %s has no %s payload", e.ID, key)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, target)
	}
	for _, e := range events {
		switch e.AggregateType {
		case "scope":
			var v Scope
			if err := decode(e, "scope", &v); err != nil {
				return err
			}
			if _, err := destination.CreateScope(ctx, v, e); err != nil {
				return err
			}
		case "entity":
			var v Entity
			if err := decode(e, "entity", &v); err != nil {
				return err
			}
			if _, err := destination.PutEntity(ctx, v, e); err != nil {
				return err
			}
		case "artifact":
			var v Artifact
			if err := decode(e, "artifact", &v); err != nil {
				return err
			}
			if _, err := destination.PutArtifact(ctx, v, e); err != nil {
				return err
			}
		case "memory":
			var v Memory
			if err := decode(e, "memory", &v); err != nil {
				return err
			}
			if _, err := destination.PutMemory(ctx, v, e); err != nil {
				return err
			}
		case "relationship":
			var v Relationship
			if err := decode(e, "relationship", &v); err != nil {
				return err
			}
			if _, err := destination.PutRelationship(ctx, v, e); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported aggregate type %q in event %s", e.AggregateType, e.ID)
		}
	}
	return nil
}
