package triplestore

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

type lenientNTParser struct {
	lex *lexer
}

func newLenientNTParser(s string) *lenientNTParser {
	return &lenientNTParser{
		lex: newLexer(s),
	}
}

func (p *lenientNTParser) parse() ([]Triple, error) {
	var tris []Triple
	var tok ntToken
	var nodeCount int
	var sub, pred, lit, datatype, langtag string
	var isLit, isResource, isSubBnode, isObjBnode, hasLangtag, hasDatatype, fullStopped bool
	var obj object

	reset := func() {
		sub, pred, lit, datatype, langtag = "", "", "", "", ""
		obj = object{}
		isLit, isResource, isSubBnode, isObjBnode, hasDatatype, hasLangtag, fullStopped = false, false, false, false, false, false, false
		nodeCount = 0
	}

	for tok.kind != EOF_TOK {
		var err error
		tok, err = p.lex.nextToken()
		if err != nil {
			return tris, err
		}
		switch tok.kind {
		case COMMENT_TOK:
			continue
		case IRI_TOK:
			nodeCount++
			switch nodeCount {
			case 1:
				sub = tok.lit
			case 2:
				pred = tok.lit
			case 3:
				isResource = true
				lit = tok.lit
			}
		case BNODE_TOK:
			nodeCount++
			switch nodeCount {
			case 1:
				sub = tok.lit
				isSubBnode = true
			case 2:
				return tris, errors.New("blank node can only be subject or object")
			case 3:
				isObjBnode = true
				lit = tok.lit
			}
		case LANGTAG_TOK:
			if nodeCount != 3 {
				return tris, errors.New("langtag misplaced")
			}
			hasLangtag = true
			langtag = tok.lit
		case LIT_TOK:
			if nodeCount != 2 {
				return tris, fmt.Errorf("tok '%s':reaching literate but missing element (node count %d)", tok.lit, nodeCount)
			}
			nodeCount++
			isLit = true
			lit = tok.lit
		case DATATYPE_TOK:
			hasDatatype = true
			datatype = tok.lit
		case FULLSTOP_TOK:
			if nodeCount != 3 {
				return tris, fmt.Errorf("reaching full stop but missing element (node count %d)", nodeCount)
			}
			fullStopped = true
			var tBuilder *tripleBuilder
			if isSubBnode {
				tBuilder = BnodePred(sub, pred)
			} else {
				tBuilder = SubjPred(sub, pred)
			}

			if isResource {
				tris = append(tris, tBuilder.Resource(lit))
			} else if isObjBnode {
				tris = append(tris, tBuilder.Bnode(lit))
			} else if isLit {
				if hasDatatype {
					obj = object{
						isLit: true,
						lit: literal{
							typ: XsdType(datatype),
							val: lit,
						},
					}
					tris = append(tris, tBuilder.Object(obj))
				} else {
					if hasLangtag {
						tris = append(tris, tBuilder.StringLiteralWithLang(lit, langtag))
					} else {
						tris = append(tris, tBuilder.StringLiteral(lit))
					}
				}
			}
			reset()
		case UNKNOWN_TOK:
			continue
		case LINEFEED_TOK:
			continue
		}
	}

	if nodeCount > 0 {
		return tris, fmt.Errorf("cannot parse line (at tok: '%s')", tok.lit)
	}

	if nodeCount != 0 && !fullStopped {
		return tris, errors.New("wrong number of elements")
	}

	return tris, nil
}

type ntTokenType int

const (
	UNKNOWN_TOK ntTokenType = iota
	IRI_TOK
	BNODE_TOK
	EOF_TOK
	WHITESPACE_TOK
	FULLSTOP_TOK
	LIT_TOK
	DATATYPE_TOK
	LANGTAG_TOK
	COMMENT_TOK
	LINEFEED_TOK
)

type ntToken struct {
	kind ntTokenType
	lit  string
}

func iriTok(s string) ntToken      { return ntToken{kind: IRI_TOK, lit: s} }
func bnodeTok(s string) ntToken    { return ntToken{kind: BNODE_TOK, lit: s} }
func litTok(s string) ntToken      { return ntToken{kind: LIT_TOK, lit: s} }
func datatypeTok(s string) ntToken { return ntToken{kind: DATATYPE_TOK, lit: s} }
func langtagTok(s string) ntToken  { return ntToken{kind: LANGTAG_TOK, lit: s} }
func commentTok(s string) ntToken  { return ntToken{kind: COMMENT_TOK, lit: s} }
func unknownTok(s string) ntToken  { return ntToken{kind: UNKNOWN_TOK, lit: s} }

var (
	wspaceTok   = ntToken{kind: WHITESPACE_TOK, lit: " "}
	fullstopTok = ntToken{kind: FULLSTOP_TOK, lit: "."}
	lineFeedTok = ntToken{kind: LINEFEED_TOK, lit: "\n"}
	eofTok      = ntToken{kind: EOF_TOK}
)

type lexer struct {
	input                  string
	position, readPosition int
	char, prevChar         rune
	width, prevWidth       int
}

func newLexer(s string) *lexer {
	return &lexer{
		input: s,
	}
}

func (l *lexer) nextToken() (ntToken, error) {
	if err := l.readChar(); err != nil {
		return ntToken{}, err
	}

	switch l.char {
	case '<':
		n, err := l.readIRI()
		return iriTok(n), err
	case '_':
		if err := l.readChar(); err != nil {
			return ntToken{}, err
		}
		if l.char != ':' {
			panic(fmt.Sprintf("invalid blank node: expecting ':', got '%c': input [%s]", l.char, l.input))
		}
		n, err := l.readBnode()
		return bnodeTok(n), err
	case ' ':
		return wspaceTok, nil
	case '.':
		return fullstopTok, nil
	case '\n':
		return lineFeedTok, nil
	case '"':
		n, err := l.readStringLiteral()
		return litTok(n), err
	case '@':
		n, err := l.readBnode()
		return langtagTok(n), err
	case '^':
		if err := l.readChar(); err != nil {
			return ntToken{}, err
		}
		if l.char == 0 {
			return eofTok, nil
		}
		if l.char != '^' {
			panic(fmt.Sprintf("invalid datatype: expecting '^', got '%c': input [%s]", l.char, l.input))
		}
		if err := l.readChar(); err != nil {
			return ntToken{}, err
		}
		if l.char == 0 {
			return eofTok, nil
		}
		if l.char != '<' {
			panic(fmt.Sprintf("invalid datatype: expecting '<', got '%c'. Input: [%s]", l.char, l.input))
		}
		n, err := l.readIRI()
		return datatypeTok(n), err
	case '#':
		l.readChar()
		n, err := l.readComment()
		return commentTok(n), err
	case 0:
		return eofTok, nil
	default:
		return unknownTok(string(l.char)), nil
	}
}

func (l *lexer) readChar() error {
	l.prevChar = l.char
	l.prevWidth = l.width

	var err error
	if l.readPosition >= len(l.input) {
		l.char = 0
	} else {
		l.char, l.width, err = decodeRune(l.input[l.readPosition:], l.readPosition)
		if err != nil {
			return err
		}
	}
	l.position = l.readPosition
	l.readPosition += l.width

	return nil
}

func (l *lexer) unreadChar() error {
	l.readPosition = l.position
	if l.position >= l.width {
		l.position = l.position - l.width
	} else {
		l.position = 0
	}
	l.char = l.prevChar
	l.width = l.prevWidth
	return nil
}

func (l *lexer) peekNextNonWithespaceChar() (found rune, count int, err error) {
	pos := l.readPosition
	if pos >= len(l.input) {
		return
	}
	var width int
	for {
		found, width, err = decodeRune(l.input[pos:], pos)
		if err != nil {
			return
		}
		count++
		if found == ' ' {
			pos = pos + width
			continue
		} else {
			return
		}
	}
}

func (l *lexer) readIRI() (string, error) {
	start := l.readPosition
	for {
		if err := l.readChar(); err != nil {
			return "", err
		}
		if l.char == '>' {
			peek, _, err := l.peekNextNonWithespaceChar()
			if err != nil {
				return "", err
			}
			if peek == 0 || peek == '<' || peek == '"' || peek == '.' || peek == '_' {
				return l.input[start:l.position], nil
			}
		}
		if l.char == 0 {
			return "", nil
		}
	}
}

func (l *lexer) readBnode() (string, error) {
	start := l.readPosition
	for {
		if err := l.readChar(); err != nil {
			return "", err
		}
		if l.char == ' ' {
			peek, _, err := l.peekNextNonWithespaceChar()
			if err != nil {
				return "", err
			}
			if peek == 0 || peek == '<' || peek == '.' {
				return l.input[start:l.position], nil
			}
		}
		if l.char == '.' {
			peek, _, err := l.peekNextNonWithespaceChar()
			if err != nil {
				return "", err
			}
			if peek == 0 || peek == '#' || peek == '\n' { // brittle: but handles <sub> <pred> _:bnode.#commenting
				s := l.input[start:l.position]
				l.unreadChar()
				return s, nil
			}
		}
		if l.char == 0 {
			return "", nil
		}
		if l.char == '<' {
			s := l.input[start:l.position]
			l.unreadChar()
			return s, nil
		}
	}
}

func (l *lexer) readStringLiteral() (string, error) {
	start := l.readPosition
	for {
		if err := l.readChar(); err != nil {
			return "", err
		}
		if l.char == '"' {
			peek, _, err := l.peekNextNonWithespaceChar()
			if err != nil {
				return "", err
			}
			if peek == 0 || peek == '.' || peek == '^' || peek == '@' {
				return l.input[start:l.position], nil
			}
		}
		if l.char == 0 {
			return "", nil
		}
	}
}

func (l *lexer) readComment() (string, error) {
	pos := l.position
	for {
		if err := l.readChar(); err != nil {
			return "", err
		}
		if l.char == '\n' {
			s := l.input[pos:l.position]
			l.unreadChar()
			return s, nil
		}
		if l.char == 0 {
			return l.input[pos:l.position], nil
		}
	}
}

func untilLineEnd(c rune) bool {
	return c != '\n' && c != 0
}

func decodeRune(s string, pos int) (r rune, width int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	r, width = utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		switch width {
		case 0:
			err = fmt.Errorf("empty utf8 char starting at position %d", pos)
			return
		case 1:
			err = fmt.Errorf("invalid utf8 encoding starting at position %d", pos)
			return
		}
	}
	return
}
