package filesystem

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

func (v Visibility) Valid() bool {
	return v == "" || v == VisibilityPrivate || v == VisibilityPublic
}

func normalizeVisibility(v Visibility) (Visibility, error) {
	if v == "" {
		return VisibilityPrivate, nil
	}
	if !v.Valid() {
		return "", ErrInvalidVisibility
	}
	return v, nil
}
