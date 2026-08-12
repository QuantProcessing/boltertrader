package lifecycle

// State is the desired lifecycle state of a gateway account. Transitions are
// linear and irreversible except for the rotation and drain excursions, so
// AllowedTransition is the single authority over what the control plane may
// request next.
type State string

const (
	StateCreating State = "creating"
	StateActive   State = "active"
	StateRotating State = "rotating"
	StateDraining State = "draining"
	StateDrained  State = "drained"
	StateDisabled State = "disabled"
	StateRemoving State = "removing"
	StateRemoved  State = "removed"
)

func AllowedTransition(from, to State) bool {
	switch from {
	case "":
		return to == StateCreating
	case StateCreating:
		return to == StateActive || to == StateDisabled
	case StateActive:
		return to == StateRotating || to == StateDraining || to == StateDisabled
	case StateRotating:
		return to == StateActive || to == StateDisabled
	case StateDraining:
		return to == StateDrained || to == StateDisabled
	case StateDrained:
		return to == StateRemoving || to == StateDisabled
	case StateDisabled:
		return to == StateRemoving || to == StateRemoved
	case StateRemoving:
		return to == StateRemoved
	case StateRemoved:
		return false
	default:
		return false
	}
}
