package main

func uniq[I any, K comparable](l []I, key func(I) K) []I {
	set := make(map[K]struct{})
	result := []I{}
	for _, item := range l {
		k := key(item)
		if _, ok := set[k]; ok {
			continue
		}
		set[k] = struct{}{}
		result = append(result, item)
	}
	return result
}

func last[I any](l []I) *I {
	length := len(l)
	if length == 0 {
		return nil
	}
	return &l[length-1]
}

func bool2int(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

// https://stackoverflow.com/a/73029665
func unpackArray[S ~[]E, E any](s S) []any {
	r := make([]any, len(s))
	for i, e := range s {
		r[i] = e
	}
	return r
}
