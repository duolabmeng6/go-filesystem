// Package pathorder compares slash-separated paths in depth-first traversal order.
package pathorder

import "strings"

// Compare orders a directory before its descendants, then proceeds to the next sibling.
func Compare(a, b string) int {
	for {
		headA, tailA, moreA := strings.Cut(a, "/")
		headB, tailB, moreB := strings.Cut(b, "/")
		if headA < headB {
			return -1
		}
		if headA > headB {
			return 1
		}
		if !moreA || !moreB {
			if moreA {
				return 1
			}
			if moreB {
				return -1
			}
			return 0
		}
		a, b = tailA, tailB
	}
}
