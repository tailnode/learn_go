package util

// 求a有b没有的key/value
func Map_diff(a, b map[string]bool) map[string]bool {
	diff := map[string]bool{}
	for key, _ := range a {
		if _, ok := b[key]; !ok {
			diff[key] = true
		}
	}
	return diff
}
