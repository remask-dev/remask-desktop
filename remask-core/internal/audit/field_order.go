package audit

import (
	"sort"
	"strconv"
	"strings"
)

// sortFields keeps request content in JSON Pointer order. Numeric path
// segments are compared as array indexes so, for example, item 2 precedes 10.
func sortFields(fields []Field) {
	sort.SliceStable(fields, func(i, j int) bool {
		return compareJSONPointers(fields[i].Path, fields[j].Path) < 0
	})
}

func compareJSONPointers(left, right string) int {
	leftParts := strings.Split(strings.TrimPrefix(left, "/"), "/")
	rightParts := strings.Split(strings.TrimPrefix(right, "/"), "/")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] == rightParts[index] {
			continue
		}
		leftNumber, leftNumeric := pathIndex(leftParts[index])
		rightNumber, rightNumeric := pathIndex(rightParts[index])
		if leftNumeric && rightNumeric && leftNumber != rightNumber {
			if leftNumber < rightNumber {
				return -1
			}
			return 1
		}
		if leftParts[index] < rightParts[index] {
			return -1
		}
		return 1
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return 0
}

func pathIndex(segment string) (int, bool) {
	if segment == "" {
		return 0, false
	}
	value, err := strconv.Atoi(segment)
	return value, err == nil && value >= 0
}
