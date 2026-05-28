package filesystem

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
	VisibilityDefault Visibility = "default"
)

func (v Visibility) Valid() bool {
	return v == "" || v == VisibilityPrivate || v == VisibilityPublic || v == VisibilityDefault
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
