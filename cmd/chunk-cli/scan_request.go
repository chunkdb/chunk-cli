package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Keep expression operators and parentheses, and keep quoted strings whole,
// so words inside an expression value cannot become reply-shaping clauses.
var replyTokenPattern = regexp.MustCompile(`'(?:[^']|'')*'|"(?:[^"]|"")*"|[a-zA-Z_][a-zA-Z_0-9]*|-?[0-9]+(?:\.[0-9]*)?(?:[eE][+-]?[0-9]+)?|[^ \t]`)

func replyTokens(statement string) []string {
	return replyTokenPattern.FindAllString(statement, -1)
}

func tokenIs(tokens []string, i int, keyword string) bool {
	return i < len(tokens) && strings.EqualFold(tokens[i], keyword)
}

func classifyScan(statement string) statementInfo {
	tokens := replyTokens(statement)
	if len(tokens) < 4 || !tokenIs(tokens, 2, "FROM") {
		return statementInfo{}
	}
	info := statementInfo{kind: statementScanIndex, table: tokens[3]}
	for i := 4; i < len(tokens); {
		switch {
		case tokenIs(tokens, i, "AFTER"):
			i++
			point := &[3]int64{}
			for axis := range point {
				if i == len(tokens) {
					break
				}
				n, err := strconv.ParseInt(tokens[i], 10, 64)
				if err != nil {
					break
				}
				point[axis] = n
				info.dims++
				i++
			}
			if info.dims != 2 && info.dims != 3 {
				return statementInfo{}
			}
			info.scanAfter = point
		case tokenIs(tokens, i, "SINCE") || tokenIs(tokens, i, "LIMIT"):
			if i+1 >= len(tokens) {
				return statementInfo{}
			}
			value, err := strconv.ParseUint(strings.TrimPrefix(tokens[i+1], "-"), 10, 64)
			if err != nil || strings.HasPrefix(tokens[i+1], "-") && value != 0 {
				return statementInfo{}
			}
			if tokenIs(tokens, i, "SINCE") {
				info.scanSince = &value
			}
			i += 2
		case tokenIs(tokens, i, "WITH") && tokenIs(tokens, i+1, "DATA"):
			info.kind = statementScanChunks
			i += 2
		case tokenIs(tokens, i, "COLUMNS"):
			i++
			enclosed := i < len(tokens) && tokens[i] == "("
			if enclosed {
				i++
			}
			start := i
			for i < len(tokens) {
				i++
				if i == len(tokens) || tokens[i] != "," {
					break
				}
				i++
			}
			var ok bool
			info.columns, ok = readColumnNames(tokens[start:i])
			if !ok {
				return statementInfo{}
			}
			if enclosed {
				if i == len(tokens) || tokens[i] != ")" {
					return statementInfo{}
				}
				i++
			}
		case tokenIs(tokens, i, "WHERE"):
			info.scanWhere = true
			i = scanConditionEnd(tokens, i+1)
		default:
			return statementInfo{}
		}
	}
	return info
}

// A trailing option starts at the top level, after an expression operand.
// An option word can also be a column name, including after an operator.
func scanConditionEnd(tokens []string, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if depth == 0 && i > start && canEndScanCondition(tokens[i-1]) {
			if tokenIs(tokens, i, "WITH") && tokenIs(tokens, i+1, "DATA") ||
				tokenIs(tokens, i, "AFTER") || tokenIs(tokens, i, "SINCE") ||
				tokenIs(tokens, i, "LIMIT") || tokenIs(tokens, i, "COLUMNS") {
				return i
			}
		}
		switch tokens[i] {
		case "(":
			depth++
		case ")":
			depth--
		}
	}
	return len(tokens)
}

func canEndScanCondition(token string) bool {
	switch strings.ToUpper(token) {
	case "AND", "OR", "NOT", "IS", "BETWEEN", "IN", "=", "<", ">", "+", "-", "*", "/", "%", ".", "$":
		return false
	default:
		return true
	}
}
