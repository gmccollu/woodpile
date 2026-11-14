package search

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Boolean terms (syntax=terms, SPEC §10.1.3): words and "quoted phrases",
// each a substring of the raw message matched as text mode matches q, joined
// by AND (or a plain space), OR and NOT, with parentheses.
//
//	expr   = or
//	or     = and { "OR" and }
//	and    = unary { [ "AND" ] unary }
//	unary  = "NOT" unary | atom
//	atom   = word | phrase | "(" expr ")"
//
// Only the upper-case words AND, OR and NOT are operators. Errors name a
// 1-based character (not byte) position.

// Limits of a terms query.
const (
	MaxTerms      = 32
	MaxTermsDepth = 8
	MaxTermsBytes = 4 << 10
)

// ErrTermsWithRegex is the message for syntax=terms with regex=true.
const ErrTermsWithRegex = "Terms and regex can't be combined: pick one."

type tokKind int

const (
	tokWord tokKind = iota
	tokPhrase
	tokLParen
	tokRParen
	tokAnd
	tokOr
	tokNot
	tokEOF
)

type token struct {
	kind tokKind
	text string // the word or the phrase's unescaped text
	pos  int    // 1-based character position of its first character
}

// termOp is a node kind of a parsed terms query.
type termOp int

const (
	opTerm termOp = iota
	opAnd
	opOr
	opNot
)

// termNode is one node of a parsed terms query.
type termNode struct {
	op   termOp
	text string      // opTerm: the word or phrase
	kids []*termNode // opAnd, opOr: two or more; opNot: one
	m    *matcher    // opTerm: its substring matcher
}

func (n *termNode) match(raw []byte) bool {
	switch n.op {
	case opTerm:
		return n.m.match(raw)
	case opNot:
		return !n.kids[0].match(raw)
	case opAnd:
		for _, k := range n.kids {
			if !k.match(raw) {
				return false
			}
		}
		return true
	default:
		for _, k := range n.kids {
			if k.match(raw) {
				return true
			}
		}
		return false
	}
}

func termsErr(format string, a ...any) error { return &QueryError{Msg: fmt.Sprintf(format, a...)} }

// tokenize splits a terms query into tokens.
func tokenize(q string) ([]token, error) {
	var out []token
	pos := 0 // characters consumed so far
	for i := 0; i < len(q); {
		r, n := utf8.DecodeRuneInString(q[i:])
		switch {
		case unicode.IsSpace(r):
			i += n
			pos++
		case r == '(' || r == ')':
			k := tokLParen
			if r == ')' {
				k = tokRParen
			}
			out = append(out, token{kind: k, pos: pos + 1})
			i += n
			pos++
		case r == '"':
			start := pos + 1
			i += n
			pos++
			var b strings.Builder
			closed := false
			for i < len(q) {
				c, cn := utf8.DecodeRuneInString(q[i:])
				i += cn
				pos++
				if c == '"' {
					closed = true
					break
				}
				if c == '\\' && i < len(q) && (q[i] == '"' || q[i] == '\\') {
					// \" and \\ are escapes; any other backslash is itself.
					b.WriteByte(q[i])
					i++
					pos++
					continue
				}
				b.WriteRune(c)
			}
			if !closed {
				return nil, termsErr("Unclosed quote starting at character %d.", start)
			}
			if b.Len() == 0 {
				return nil, termsErr("Empty phrase at character %d.", start)
			}
			out = append(out, token{kind: tokPhrase, text: b.String(), pos: start})
		default:
			start := pos + 1
			j := i
			for j < len(q) {
				c, cn := utf8.DecodeRuneInString(q[j:])
				if unicode.IsSpace(c) || c == '(' || c == ')' {
					break
				}
				j += cn
				pos++
			}
			w := q[i:j]
			i = j
			k := tokWord
			switch w {
			case "AND":
				k = tokAnd
			case "OR":
				k = tokOr
			case "NOT":
				k = tokNot
			}
			out = append(out, token{kind: k, text: w, pos: start})
		}
	}
	return append(out, token{kind: tokEOF, pos: pos + 1}), nil
}

type termsParser struct {
	toks  []token
	i     int
	depth int
	cs    bool
}

func (p *termsParser) peek() token { return p.toks[p.i] }
func (p *termsParser) next() token { t := p.toks[p.i]; p.i++; return t }

func startsUnary(k tokKind) bool {
	return k == tokWord || k == tokPhrase || k == tokLParen || k == tokNot
}

func opName(k tokKind) string {
	if k == tokAnd {
		return "AND"
	}
	return "OR"
}

func needBoth(t token) error {
	return termsErr("%q at character %d needs a term on both sides.", opName(t.kind), t.pos)
}

func (p *termsParser) parseOr() (*termNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		op := p.next()
		if left == nil {
			return nil, needBoth(op)
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, needBoth(op)
		}
		left = join(opOr, left, right)
	}
	return left, nil
}

func (p *termsParser) parseAnd() (*termNode, error) {
	if p.peek().kind == tokAnd {
		return nil, needBoth(p.peek())
	}
	left, err := p.parseUnary()
	if err != nil || left == nil {
		return nil, err
	}
	for {
		k := p.peek().kind
		var right *termNode
		switch {
		case k == tokAnd:
			op := p.next()
			if right, err = p.parseUnary(); err != nil {
				return nil, err
			}
			if right == nil {
				return nil, needBoth(op)
			}
		case startsUnary(k):
			if right, err = p.parseUnary(); err != nil {
				return nil, err
			}
		default:
			return left, nil
		}
		left = join(opAnd, left, right)
	}
}

func (p *termsParser) parseUnary() (*termNode, error) {
	t := p.peek()
	switch t.kind {
	case tokNot:
		p.next()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if x == nil {
			return nil, termsErr(`"NOT" at character %d needs a term after it.`, t.pos)
		}
		return &termNode{op: opNot, kids: []*termNode{x}}, nil
	case tokWord, tokPhrase:
		p.next()
		m, err := newMatcher(t.text, false, p.cs)
		if err != nil {
			return nil, err
		}
		return &termNode{op: opTerm, text: t.text, m: m}, nil
	case tokLParen:
		p.next()
		p.depth++
		if p.depth > MaxTermsDepth {
			return nil, termsErr("Parentheses nested deeper than %d.", MaxTermsDepth)
		}
		if p.peek().kind == tokRParen {
			return nil, termsErr("Empty parentheses at character %d.", t.pos)
		}
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen || e == nil {
			// parseOr stops only at ")" or the end.
			return nil, termsErr(`Missing ")" for the "(" at character %d.`, t.pos)
		}
		p.next()
		p.depth--
		return e, nil
	}
	return nil, nil
}

// join combines a and b under op, flattening a chain of the same operator.
func join(op termOp, a, b *termNode) *termNode {
	if a.op == op {
		a.kids = append(a.kids, b)
		return a
	}
	return &termNode{op: op, kids: []*termNode{a, b}}
}

// parseTerms parses a terms query. A nil node (and no error) means the query
// has no terms, which matches every line.
func parseTerms(q string, caseSensitive bool) (*termNode, error) {
	if len(q) > MaxTermsBytes {
		return nil, termsErr("The query is longer than 4 KiB.")
	}
	toks, err := tokenize(q)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, t := range toks {
		if t.kind == tokWord || t.kind == tokPhrase {
			n++
		}
	}
	if n > MaxTerms {
		return nil, termsErr("Too many terms (%d); the limit is %d.", n, MaxTerms)
	}
	p := &termsParser{toks: toks, cs: caseSensitive}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, termsErr(`Unmatched ")" at character %d.`, t.pos)
	}
	return e, nil
}

// newTermsMatcher compiles a terms query into a matcher. A single term is a
// plain substring matcher; otherwise the matcher evaluates the expression, and
// prefilters on the longest term every match must contain (a positive term on
// the top-level AND chain), when there is one.
func newTermsMatcher(q string, caseSensitive bool) (*matcher, error) {
	e, err := parseTerms(q, caseSensitive)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return &matcher{kind: matchAll}, nil
	}
	if e.op == opTerm {
		return e.m, nil
	}
	m := &matcher{kind: matchTerms, terms: e}
	if e.op == opAnd {
		for _, k := range e.kids {
			if k.op == opTerm && k.m.prefilter && (m.pre == nil || len(k.m.needle) > len(m.pre.needle)) {
				m.pre = k.m
			}
		}
		m.prefilter = m.pre != nil
	}
	return m, nil
}

// TermsPositive returns the words and phrases of a terms query that aren't
// under a NOT (what highlighting marks), or nil for an invalid query.
func TermsPositive(q string) []string {
	e, err := parseTerms(q, false)
	if err != nil || e == nil {
		return nil
	}
	var out []string
	var walk func(n *termNode)
	walk = func(n *termNode) {
		switch n.op {
		case opTerm:
			out = append(out, n.text)
		case opNot:
		default:
			for _, k := range n.kids {
				walk(k)
			}
		}
	}
	walk(e)
	return out
}
