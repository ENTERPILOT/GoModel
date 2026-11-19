package longspeech

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{name: "blank text has no parts", text: " \n\t ", limit: 10, want: nil},
		{name: "text that fits is returned untouched", text: " Hello there. ", limit: 14, want: []string{" Hello there. "}},
		{name: "no limit", text: "One. Two. Three.", limit: 0, want: []string{"One. Two. Three."}},
		{
			name:  "packs whole sentences up to the limit",
			text:  "One two. Three four. Five six.",
			limit: 20,
			want:  []string{"One two. Three four.", "Five six."},
		},
		{
			name:  "question and exclamation marks end sentences",
			text:  "Is it done? Yes! Ship it now.",
			limit: 16,
			want:  []string{"Is it done? Yes!", "Ship it now."},
		},
		{
			name:  "titles do not end a sentence",
			text:  "Dr. Smith met Mr. Jones today. They talked.",
			limit: 32,
			want:  []string{"Dr. Smith met Mr. Jones today.", "They talked."},
		},
		{
			name:  "a title is not preferred over a word break",
			text:  "Dr. Smith met Mr. Jones today.",
			limit: 12,
			want:  []string{"Dr. Smith", "met Mr.", "Jones today."},
		},
		{
			name:  "initials stay with the name",
			text:  "J. K. Rowling wrote it. Then more.",
			limit: 25,
			want:  []string{"J. K. Rowling wrote it.", "Then more."},
		},
		{
			name:  "dotted shorthand does not end a sentence",
			text:  "Use a tool, e.g. a hammer. Then rest.",
			limit: 28,
			want:  []string{"Use a tool, e.g. a hammer.", "Then rest."},
		},
		{
			name:  "capitalized dotted shorthand does not end a sentence",
			text:  "The U.S. Senate voted today. It passed.",
			limit: 30,
			want:  []string{"The U.S. Senate voted today.", "It passed."},
		},
		{
			name:  "an ordinal number does not end a sentence",
			text:  "Am 3. Mai kam er an. Dann ging er.",
			limit: 22,
			want:  []string{"Am 3. Mai kam er an.", "Dann ging er."},
		},
		{
			name:  "polish shorthand before a capitalized word",
			text:  "Mieszka przy ul. Długiej, np. Kowalski. Wraca jutro.",
			limit: 40,
			want:  []string{"Mieszka przy ul. Długiej, np. Kowalski.", "Wraca jutro."},
		},
		{
			name:  "a period followed by lowercase is an abbreviation",
			text:  "It takes approx. five minutes. Wait here.",
			limit: 32,
			want:  []string{"It takes approx. five minutes.", "Wait here."},
		},
		{
			name:  "decimals are not split",
			text:  "Pi is 3.14159 and e is 2.71828. Next sentence here.",
			limit: 34,
			want:  []string{"Pi is 3.14159 and e is 2.71828.", "Next sentence here."},
		},
		{
			name:  "a sentence may end in a decimal",
			text:  "Pi is about 3.14. Tau is twice that.",
			limit: 20,
			want:  []string{"Pi is about 3.14.", "Tau is twice that."},
		},
		{
			name:  "thousands separators are not clause breaks",
			text:  "It costs 1,000,000 dollars, give or take.",
			limit: 20,
			want:  []string{"It costs 1,000,000", "dollars,", "give or take."},
		},
		{
			name:  "hosts, queries and times are not split",
			text:  "See example.com/docs?page=2 at 12:30. Done here.",
			limit: 40,
			want:  []string{"See example.com/docs?page=2 at 12:30.", "Done here."},
		},
		{
			name:  "closing quotes stay with their sentence",
			text:  `"Really?" she asked. "Yes." He left.`,
			limit: 22,
			want:  []string{`"Really?" she asked.`, `"Yes." He left.`},
		},
		{
			name:  "a german closing quote ends its sentence",
			text:  "Er rief: „Halt.“ Dann ging er weiter.",
			limit: 30,
			want:  []string{"Er rief: „Halt.“", "Dann ging er weiter."},
		},
		{
			name:  "an ellipsis followed by lowercase continues the sentence",
			text:  "Wait… what happened here? Nothing at all.",
			limit: 28,
			want:  []string{"Wait… what happened here?", "Nothing at all."},
		},
		{
			name:  "a run of terminators is kept together",
			text:  "What?! No way... Fine then. Go.",
			limit: 17,
			want:  []string{"What?! No way...", "Fine then. Go."},
		},
		{
			name:  "a long sentence is cut at a clause before a word",
			text:  "When the rain stopped, we walked home slowly and quietly",
			limit: 30,
			want:  []string{"When the rain stopped,", "we walked home slowly and", "quietly"},
		},
		{
			name:  "a line break beats a clause",
			text:  "Chapter one, the start\nIt was a dark and stormy night",
			limit: 30,
			want:  []string{"Chapter one, the start", "It was a dark and stormy night"},
		},
		{
			name:  "a word longer than the limit is cut",
			text:  "aaaaaaaaaa",
			limit: 4,
			want:  []string{"aaaa", "aaaa", "aa"},
		},
		{
			name:  "chinese sentences need no space",
			text:  "今天天气很好。我们去公园吧！你觉得呢？好的。",
			limit: 10,
			want:  []string{"今天天气很好。", "我们去公园吧！", "你觉得呢？好的。"},
		},
		{
			name:  "a cjk closing bracket stays with its sentence",
			text:  "他说：「你好。」然后离开了。再见。",
			limit: 12,
			want:  []string{"他说：「你好。」", "然后离开了。再见。"},
		},
		{
			name:  "ascii punctuation in unspaced chinese",
			text:  "你好吗?我很好!谢谢.",
			limit: 5,
			want:  []string{"你好吗?", "我很好!", "谢谢."},
		},
		{
			name:  "japanese falls back to the ideographic comma",
			text:  "吾輩は猫である、名前はまだ無い、どこで生れたか",
			limit: 12,
			want:  []string{"吾輩は猫である、", "名前はまだ無い、", "どこで生れたか"},
		},
		{
			name:  "devanagari danda",
			text:  "यह अच्छा है। वह भी ठीक है। चलो।",
			limit: 16,
			want:  []string{"यह अच्छा है।", "वह भी ठीक है।", "चलो।"},
		},
		{
			name:  "arabic question mark",
			text:  "كيف حالك؟ أنا بخير. شكرا.",
			limit: 12,
			want:  []string{"كيف حالك؟", "أنا بخير.", "شكرا."},
		},
		{
			name:  "combining marks stay on their base character",
			text:  "ééé",
			limit: 3,
			want:  []string{"é", "é", "é"},
		},
		{
			name:  "a cut may fall right after the first character",
			text:  "ae\u0301b",
			limit: 2,
			want:  []string{"a", "e\u0301", "b"},
		},
		{
			name:  "a flag is not cut apart",
			text:  "a🇩🇪🇵🇱b",
			limit: 4,
			want:  []string{"a🇩🇪", "🇵🇱b"},
		},
		{
			name:  "a tag sequence flag is not cut apart",
			text:  "ab🏴󠁧󠁢󠁥󠁮󠁧󠁿c",
			limit: 8,
			want:  []string{"ab", "🏴󠁧󠁢󠁥󠁮󠁧󠁿c"},
		},
		{
			name:  "a file name ends a sentence",
			text:  "Open main.go. Then run it.",
			limit: 16,
			want:  []string{"Open main.go.", "Then run it."},
		},
		{
			name:  "a joined emoji is not cut apart",
			text:  "ab👨‍👩‍👧cd",
			limit: 6,
			want:  []string{"ab", "👨‍👩‍👧c", "d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Split(tt.text, tt.limit))
		})
	}
}

// TestSplit_KeepsEveryCharacter cuts a mixed-script text at every limit and
// checks what no table can: nothing but whitespace is lost or reordered, and
// no part exceeds the limit or carries whitespace at its edges.
func TestSplit_KeepsEveryCharacter(t *testing.T) {
	text := "Dr. Ada wrote: \"Is 3.14 enough?\" No… e.g. the U.S. used 1,000 digits!\n\n" +
		"第二段。「引用です。」それから、次へ進む？はい！\n" +
		"- item one\n- item two\tand a tab\r\n" +
		"Zażółć gęślą jaźń. Ελληνικά; кириллица: да. 👨‍👩‍👧 énd."
	unspaced := func(parts ...string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, strings.Join(parts, ""))
	}
	for limit := 1; limit <= utf8.RuneCountInString(text); limit++ {
		parts := Split(text, limit)
		require.Equal(t, unspaced(text), unspaced(parts...), "limit %d", limit)
		for _, part := range parts {
			require.LessOrEqual(t, utf8.RuneCountInString(part), limit, "limit %d part %q", limit, part)
			require.NotEmpty(t, part, "limit %d", limit)
			require.Equal(t, strings.TrimSpace(part), part, "limit %d", limit)
		}
	}
}

// TestSplit_StaysLinear guards the bounded scans: input made only of
// characters that send every boundary check looking backwards must still be
// split in one pass.
func TestSplit_StaysLinear(t *testing.T) {
	for _, filler := range []string{")", ".", "a.", "\"", " "} {
		text := "x" + strings.Repeat(filler, 400_000)
		parts := Split(text, 4096)
		for _, part := range parts {
			require.LessOrEqual(t, utf8.RuneCountInString(part), 4096)
		}
	}
}
