package players

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrBadName: too short or long, odd characters, or a word the filter keeps out.
var ErrBadName = errors.New("bad_name")

const (
	NameMin = 2
	NameMax = 24
)

// CleanName checks a name a player typed and returns it tidied (spaces collapsed). Players
// are families in Uzbekistan (players.md): names are checked against rude words in Uzbek,
// Russian and English, written in either script or with digits for letters, and against
// names that pose as the game's staff.
func CleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	n := utf8.RuneCountInString(name)
	if n < NameMin || n > NameMax {
		return "", ErrBadName
	}
	letters := 0
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			letters++
		case r == ' ', r == '_', r == '-', r == '.', r == '\'', r == '‘', r == '’', r == 'ʻ', r == 'ʼ':
		case r == '‍', r == '️', unicode.Is(unicode.So, r), unicode.Is(unicode.Sk, r): // emoji
		default:
			return "", ErrBadName
		}
	}
	if letters == 0 || Rude(name) {
		return "", ErrBadName
	}
	return name, nil
}

// Rude reports whether a name contains a word the filter keeps out.
func Rude(name string) bool {
	glued, words := fold(name)
	for _, root := range rudeRoots {
		if strings.Contains(glued, root) {
			return true
		}
	}
	for _, w := range words {
		if rudeWords[w] {
			return true
		}
	}
	return false
}

// fold lowers a name, turns Cyrillic and digit look-alikes into Latin letters and drops
// doubled letters, so "Х у й", "xuy", "SUKA", "c y k a" and "5uk4" all read the same. It
// returns the whole name glued together (for roots) and its words (for short words that
// would otherwise match inside harmless ones).
func fold(name string) (string, []string) {
	var b strings.Builder
	var words []string
	var word strings.Builder
	var last rune
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(name) {
		s, ok := lookalike[r]
		if !ok {
			switch {
			case r >= 'a' && r <= 'z':
				s = string(r)
			default:
				flush()
				last = 0
				continue
			}
		}
		for _, c := range s {
			if c == last {
				continue
			}
			last = c
			b.WriteRune(c)
			word.WriteRune(c)
		}
	}
	flush()
	return b.String(), words
}

var lookalike = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "j", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "x", 'ц': "s", 'ч': "ch", 'ш': "sh", 'щ': "sh",
	'ъ': "", 'ы': "i", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya", 'ў': "o", 'қ': "q", 'ғ': "g", 'ҳ': "h",
	'0': "o", '1': "i", '3': "e", '4': "a", '5': "s", '6': "b", '7': "t", '8': "b", '9': "g",
	'@': "a", '$': "s", '!': "i", '|': "i",
	'ı': "i", 'ö': "o", 'ü': "u", 'ç': "ch", 'ş': "sh", 'ğ': "g",
	// spaces, dots, dashes and quotes between letters do not split a root ("s.u.k.a")
	' ': "", '.': "", '-': "", '_': "", '\'': "", '‘': "", '’': "", 'ʻ': "", 'ʼ': "", '*': "",
}

// rudeRoots match anywhere in the folded name. Only roots that do not hide inside ordinary
// Uzbek, Russian or English names and words (no "nazi": Nazira; no "manda": Amanda).
var rudeRoots = foldAll(
	// English
	"fuck", "shit", "bitch", "cunt", "whore", "slut", "nigger", "faggot", "porn", "pussy",
	"dick", "penis", "vagina", "hitler", "asshole",
	// Russian, as it reads once folded to Latin
	"xuy", "xui", "huy", "xues", "pizd", "pisd", "yeban", "yebat", "eblan", "ebanut", "blya",
	"suka", "suchk", "mudak", "mudil", "pidor", "pidar", "gandon", "zalup", "shlux", "shluh",
	"droch", "dolboeb", "debil", "uebok",
	// Uzbek
	"jalab", "qanjiq", "kanjik", "sikay", "sikish", "siktir", "qotaq", "kotak", "dalbayob",
	"dalbaeb", "onangni", "xunasa", "haromzoda", "xaromzoda", "itvachcha",
	// posing as the game's staff
	"admin", "moderator", "shafaqgeo", "support",
)

// rudeWords match whole words only: short ones that would match inside harmless words.
var rudeWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range foldAll("sik", "skay", "sex", "seks", "am", "ass", "fag", "lox", "loh",
		"ebat", "eban", "ebal", "manda", "nazi", "rape", "allah", "alloh") {
		m[w] = true
	}
	return m
}()

func foldAll(words ...string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i], _ = fold(w)
	}
	return out
}
