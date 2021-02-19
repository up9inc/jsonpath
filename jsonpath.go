// Package jsonpath implements Stefan Goener's JSONPath http://goessner.net/articles/JsonPath/
//
// A jsonpath applies to any JSON decoded data using interface{} when
// decoded with encoding/json (http://golang.org/pkg/encoding/json/) :
//
//    var bookstore interface{}
//    err := json.Unmarshal(data, &bookstore)
//    authors, err := jsonpath.Get(bookstore, "$..authors")
//
// A jsonpath expression can be prepared to be reused multiple times :
//
//    allAuthors, err := jsonpath.Prepare("$..authors")
//    ...
//    var bookstore interface{}
//    err = json.Unmarshal(data, &bookstore)
//    authors, err := allAuthors(bookstore)
//
// The type of the values returned by the `Get` method or `Prepare`
// functions depends on the jsonpath expression.
//
// Limitations
//
// No support for subexpressions and filters.
// Strings in brackets must use double quotes.
// It cannot operate on JSON decoded struct fields.
//
package jsonpath

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/scanner"
)

// Get a path from a decoded JSON array or object ([]interface{} or map[string]interface{})
// and returns the corresponding value or an error.
//
// The returned value type depends on the requested path and the JSON value.
func Get(value interface{}, path string) (interface{}, error) {
	_, filter, err := Prepare(path)
	if err != nil {
		return nil, err
	}
	return filter(value)
}

func Set(value interface{}, path string, update interface{}) (interface{}, error) {
	p, filter, err := Prepare(path)
	p.update = update
	if err != nil {
		return nil, err
	}
	return filter(value)
}

// Prepare will parse the path and return a filter function that can then be applied to decoded JSON values.
func Prepare(path string) (*parser, FilterFunc, error) {
	p := newScanner(path)
	if err := p.parse(); err != nil {
		return p, nil, err
	}
	return p, p.prepareFilterFunc(), nil
}

// FilterFunc applies a prepared json path to a JSON decoded value
type FilterFunc func(value interface{}) (interface{}, error)

// short variables
// p: the parser context
// r: root node => @
// c: current node => $
// a: the list of actions to apply next
// v: value

// actionFunc applies a transformation to current value (possibility using root)
// then applies the next action from actions (using next()) to the output of the transformation
type actionFunc func(r, c interface{}, a actions) (interface{}, error)

// a list of action functions to apply one after the other
type actions []actionFunc

// next applies the next action function
func (a actions) next(r, c interface{}) (interface{}, error) {
	return a[0](r, c, a[1:])
}

// call applies the next action function without taking it out
func (a actions) call(r, c interface{}) (interface{}, error) {
	return a[0](r, c, a)
}

type exprFunc func(r, c interface{}) (interface{}, error)

type searchResults []interface{}

func (sr searchResults) append(v interface{}) searchResults {
	if vsr, ok := v.(searchResults); ok {
		return append(sr, vsr...)
	}
	return append(sr, v)
}

type parser struct {
	scanner scanner.Scanner
	path    string
	actions actions
	update  interface{}
}

func (p *parser) prepareFilterFunc() FilterFunc {
	actions := p.actions
	return func(value interface{}) (interface{}, error) {
		result, err := actions.next(value, value)
		if err == nil {
			if sr, ok := result.(searchResults); ok {
				result = ([]interface{})(sr)
			}
		}
		return result, err
	}
}

func newScanner(path string) *parser {
	path = strings.ReplaceAll(path, "['", "[\"")
	path = strings.ReplaceAll(path, "']", "\"]")
	return &parser{path: path, update: nil}
}

func (p *parser) scan() rune {
	return p.scanner.Scan()
}

func (p *parser) text() string {
	return p.scanner.TokenText()
}

func (p *parser) column() int {
	return p.scanner.Position.Column
}

func (p *parser) peek() rune {
	return p.scanner.Peek()
}

func (p *parser) add(action actionFunc) {
	p.actions = append(p.actions, action)
}

func (p *parser) parse() error {
	p.scanner.Init(strings.NewReader(p.path))
	if p.scan() != '$' {
		return errors.New("path must start with a '$'")
	}
	return p.parsePath()
}

func (p *parser) parsePath() (err error) {
	for err == nil {
		switch p.scan() {
		case '.':
			p.scanner.Mode = scanner.ScanIdents
			switch p.scan() {
			case scanner.Ident:
				err = p.parseObjAccess(false)
			case '*':
				err = p.prepareWildcard()
			case '.':
				err = p.parseDeep()
			case '[':
				err = p.parseObjAccess(true)
			default:
				err = fmt.Errorf("expected JSON child identifier after '.' at %d", p.column())
			}
		case '[':
			err = p.parseBracket()
		case scanner.EOF:
			// the end, add a last func that just return current node
			p.add(func(r, c interface{}, a actions) (interface{}, error) { return c, nil })
			return nil
		default:
			err = fmt.Errorf("unexpected token %s at %d", p.text(), p.column())
		}
	}
	return
}

func (p *parser) parseObjAccess(bracket bool) error {
	mode_backup := p.scanner.Mode
	if bracket {
		p.scanner.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanInts
		p.scan() // eat [
	}
	ident := p.text()
	if bracket {
		ident = ident[1:len(ident)-1]
		p.scan() // eat ]
		p.scanner.Mode = mode_backup
	}
	column := p.scanner.Position.Column
	p.add(func(r, c interface{}, a actions) (interface{}, error) {
		obj, ok := c.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("expected JSON object to access child '%s' at %d", ident, column)
		}
		if c, ok = obj[ident]; !ok {
			return nil, fmt.Errorf("child '%s' not found in JSON object at %d", ident, column)
		}
		if (column + len(ident)) > len(p.path) && p.update != nil {
			obj[ident] = p.update
		}
		return a.next(r, c)
	})
	return nil
}

func (p *parser) prepareWildcard() error {
	column := p.scanner.Position.Column
	p.add(func(r, c interface{}, a actions) (interface{}, error) {
		values := searchResults{}
		if obj, ok := c.(map[string]interface{}); ok {
			for k, _ := range obj {
				if column == len(p.path) && p.update != nil {
					obj[k] = p.update
				}
			}
			for _, v := range valuesSortedByKey(obj) {
				v, err := a.next(r, v)
				if err != nil {
					continue
				}
				values = values.append(v)
			}
		} else if array, ok := c.([]interface{}); ok {
			for i, v := range array {
				v, err := a.next(r, v)
				if err != nil {
					continue
				}
				if column == len(p.path) && p.update != nil {
					array[i] = p.update
				}
				values = values.append(v)
			}
		}
		return values, nil
	})
	return nil
}

func (p *parser) parseDeep() (err error) {
	p.scanner.Mode = scanner.ScanIdents
	switch p.scan() {
	case scanner.Ident:
		p.add(func(r, c interface{}, a actions) (interface{}, error) {
			return recSearchParent(r, c, a, searchResults{}), nil
		})
		return p.parseObjAccess(false)
	case '[':
		p.add(func(r, c interface{}, a actions) (interface{}, error) {
			return recSearchParent(r, c, a, searchResults{}), nil
		})
		return p.parseBracket()
	case '*':
		p.add(func(r, c interface{}, a actions) (interface{}, error) {
			return recSearchChildren(r, c, a, searchResults{}), nil
		})
		p.add(func(r, c interface{}, a actions) (interface{}, error) {
			return a.next(r, c)
		})
		return nil
	case scanner.EOF:
		return fmt.Errorf("cannot end with a scan '..' at %d", p.column())
	default:
		return fmt.Errorf("unexpected token '%s' after deep search '..' at %d",
			p.text(), p.column())
	}
}

// bracket contains filter, wildcard or array access
func (p *parser) parseBracket() error {
	if p.peek() == '?' {
		return p.parseFilter()
	} else if p.peek() == '*' {
		p.scan() // eat *
		if p.scan() != ']' {
			return fmt.Errorf("expected closing bracket after [* at %d", p.column())
		}
		return p.prepareWildcard()
	}
	return p.parseArray()
}

// array contains either a union [,,,], a slice [::] or a single element.
// Each element can be an int, a string or an expression.
// TODO optimize map/array access (by detecting the type of indexes)
func (p *parser) parseArray() error {
	var indexes []interface{} // string, int or exprFunc
	var mode string           // slice or union
	p.scanner.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanInts
parse:
	for {
		// parse value
		switch p.scan() {
		case scanner.Int:
			index, err := strconv.Atoi(p.text())
			if err != nil {
				return fmt.Errorf("%s at %d", err.Error(), p.column())
			}
			indexes = append(indexes, index)
		case '-':
			if p.scan() != scanner.Int {
				return fmt.Errorf("expect an int after the minus '-' sign at %d", p.column())
			}
			index, err := strconv.Atoi(p.text())
			if err != nil {
				return fmt.Errorf("%s at %d", err.Error(), p.column())
			}
			indexes = append(indexes, -index)
		case scanner.Ident:
			indexes = append(indexes, p.text())
		case scanner.String:
			s, err := strconv.Unquote(p.text())
			if err != nil {
				return fmt.Errorf("bad string %s at %d", err, p.column())
			}
			indexes = append(indexes, s)
		case '(':
			filter, err := p.parseExpression()
			if err != nil {
				return err
			}
			indexes = append(indexes, filter)
		case ':': // when slice value is omitted
			if mode == "" {
				mode = "slice"
				indexes = append(indexes, 0)
			} else if mode == "slice" {
				indexes = append(indexes, 0)
			} else {
				return fmt.Errorf("unexpected ':' after %s at %d", mode, p.column())
			}
			continue // skip separator parsing, it's done
		case ']': // when slice value is omitted
			if mode == "slice" {
				indexes = append(indexes, 0)
			} else if len(indexes) == 0 {
				return fmt.Errorf("expected at least one key, index or expression at %d", p.column())
			}
			break parse
		case scanner.EOF:
			return fmt.Errorf("unexpected end of path at %d", p.column())
		default:
			return fmt.Errorf("unexpected token '%s' at %d", p.text(), p.column())
		}
		// parse separator
		switch p.scan() {
		case ',':
			if mode == "" {
				mode = "union"
			} else if mode != "union" {
				return fmt.Errorf("unexpeted ',' in %s at %d", mode, p.column())
			}
		case ':':
			if mode == "" {
				mode = "slice"
			} else if mode != "slice" {
				return fmt.Errorf("unexpected ':' in %s at %d", mode, p.column())
			}
		case ']':
			break parse
		case scanner.EOF:
			return fmt.Errorf("unexpected end of path at %d", p.column())
		default:
			return fmt.Errorf("unexpected token '%s' at %d", p.text(), p.column())
		}
	}
	if mode == "slice" {
		if len(indexes) > 3 {
			return fmt.Errorf("bad range syntax [start:end:step] at %d", p.column())
		}
		p.add(prepareSlice(indexes, p.column()))
	} else if len(indexes) == 1 {
		p.add(prepareIndex(indexes[0], p.column()))
	} else {
		p.add(prepareUnion(indexes, p.column()))
	}
	return nil
}

func (p *parser) parseFilter() error {
	return errors.New("Filters are not (yet) implemented")
}

func (p *parser) parseExpression() (exprFunc, error) {
	return nil, errors.New("Expression are not (yet) implemented")
}

func recSearchParent(r, c interface{}, a actions, acc searchResults) searchResults {
	if v, err := a.next(r, c); err == nil {
		acc = acc.append(v)
	}
	return recSearchChildren(r, c, a, acc)
}

func recSearchChildren(r, c interface{}, a actions, acc searchResults) searchResults {
	if obj, ok := c.(map[string]interface{}); ok {
		for _, c := range valuesSortedByKey(obj) {
			acc = recSearchParent(r, c, a, acc)
		}
	} else if array, ok := c.([]interface{}); ok {
		for _, c := range array {
			acc = recSearchParent(r, c, a, acc)
		}
	}
	return acc
}

func prepareIndex(index interface{}, column int) actionFunc {
	return func(r, c interface{}, a actions) (interface{}, error) {
		if obj, ok := c.(map[string]interface{}); ok {
			key, err := indexAsString(index, r, c)
			if err != nil {
				return nil, err
			}
			if c, ok = obj[key]; !ok {
				return nil, fmt.Errorf("no key '%s' for object at %d", key, column)
			}
			return a.next(r, c)
		} else if array, ok := c.([]interface{}); ok {
			index, err := indexAsInt(index, r, c)
			if err != nil {
				return nil, err
			}
			if index < 0 || index >= len(array) {
				return nil, fmt.Errorf("out of bound array access at %d", column)
			}
			return a.next(r, array[index])
		}
		return nil, fmt.Errorf("expected array or object at %d", column)
	}
}

func prepareSlice(indexes []interface{}, column int) actionFunc {
	return func(r, c interface{}, a actions) (interface{}, error) {
		array, ok := c.([]interface{})
		if !ok {
			return nil, fmt.Errorf("expected JSON array at %d", column)
		}
		var err error
		var start, end, step int
		if start, err = indexAsInt(indexes[0], r, c); err != nil {
			return nil, err
		}
		if end, err = indexAsInt(indexes[1], r, c); err != nil {
			return nil, err
		}
		if len(indexes) > 2 {
			if step, err = indexAsInt(indexes[2], r, c); err != nil {
				return nil, err
			}
		}
		max := len(array)
		start = negmax(start, max)
		if end == 0 {
			end = max
		} else {
			end = negmax(end, max)
		}
		if start > end {
			return nil, fmt.Errorf("cannot start range at %d and end at %d", start, end)
		}
		if step == 0 {
			step = 1
		}
		var values searchResults
		if step > 0 {
			for i := start; i < end; i += step {
				v, err := a.next(r, array[i])
				if err != nil {
					continue
				}
				values = values.append(v)
			}
		} else { // reverse order on negative step
			for i := end - 1; i >= start; i += step {
				v, err := a.next(r, array[i])
				if err != nil {
					continue
				}
				values = values.append(v)
			}
		}
		return values, nil
	}
}

func prepareUnion(indexes []interface{}, column int) actionFunc {
	return func(r, c interface{}, a actions) (interface{}, error) {
		if obj, ok := c.(map[string]interface{}); ok {
			var values searchResults
			for _, index := range indexes {
				key, err := indexAsString(index, r, c)
				if err != nil {
					return nil, err
				}
				if c, ok = obj[key]; !ok {
					return nil, fmt.Errorf("no key '%s' for object at %d", key, column)
				}
				if c, err = a.next(r, c); err != nil {
					return nil, err
				}
				values = values.append(c)
			}
			return values, nil
		} else if array, ok := c.([]interface{}); ok {
			var values searchResults
			for _, index := range indexes {
				index, err := indexAsInt(index, r, c)
				if err != nil {
					return nil, err
				}
				if index < 0 || index >= len(array) {
					return nil, fmt.Errorf("out of bound array access at %d", column)
				}
				if c, err = a.next(r, array[index]); err != nil {
					return nil, err
				}
				values = values.append(c)
			}
			return values, nil
		}
		return nil, fmt.Errorf("expected array or object at %d", column)
	}
}

func negmax(n, max int) int {
	if n < 0 {
		n = max + n
		if n < 0 {
			n = 0
		}
	} else if n > max {
		return max
	}
	return n
}

func indexAsInt(index, r, c interface{}) (int, error) {
	switch i := index.(type) {
	case int:
		return i, nil
	case exprFunc:
		index, err := i(r, c)
		if err != nil {
			return 0, err
		}
		switch i := index.(type) {
		case int:
			return i, nil
		default:
			return 0, fmt.Errorf("expected expression to return an index for array access")
		}
	default:
		return 0, fmt.Errorf("expected index value (integer or expression returning an integer) for array access")
	}
}

func indexAsString(key, r, c interface{}) (string, error) {
	switch s := key.(type) {
	case string:
		return s, nil
	case exprFunc:
		key, err := s(r, c)
		if err != nil {
			return "", err
		}
		switch s := key.(type) {
		case string:
			return s, nil
		default:
			return "", fmt.Errorf("expected expression to return a key for object access")
		}
	default:
		return "", fmt.Errorf("expected key value (string or expression returning a string) for object access")
	}
}

func valuesSortedByKey(m map[string]interface{}) []interface{} {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]interface{}, 0, len(m))
	for _, k := range keys {
		values = append(values, m[k])
	}
	return values
}
