package markdown

import "testing"

// TestMathToUnicode is the frozen §F.1 table from workflow 015 — binding,
// exact input and expected output bytes.
func TestMathToUnicode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "money dollars",
			in:   "costs $5 and $6 total",
			want: "costs $5 and $6 total",
		},
		{
			name: "shell command substitution",
			in:   `run $(ip -o link show | awk '/x/ {print $2}') now`,
			want: `run $(ip -o link show | awk '/x/ {print $2}') now`,
		},
		{
			name: "code span masks math",
			in:   "keep `$x^2$` literal",
			want: "keep `$x^2$` literal",
		},
		{
			name: "escaped dollars",
			in:   `pay \$5 or \$10`,
			want: `pay \$5 or \$10`,
		},
		{
			name: "no tex construct gate",
			in:   "$hello world$",
			want: "$hello world$",
		},
		{
			name: "bad close",
			in:   "$a, $b",
			want: "$a, $b",
		},
		{
			name: "prime and inverse superscript",
			in:   "$v' = qvq^{-1}$",
			want: "v′ = qvq⁻¹",
		},
		{
			name: "quaternion display line",
			in:   "$$q = q_0 + q_1\\mathbf{i} + q_2\\mathbf{j} + q_3\\mathbf{k}, \\qquad \\mathbf{i}^2 = \\mathbf{j}^2 = \\mathbf{k}^2 = \\mathbf{ijk} = -1$$ ^[raw/articles/quaternion.md]",
			want: "q = q₀ + q₁**i** + q₂**j** + q₃**k**,  **i**² = **j**² = **k**² = **ijk** = −1 ^[raw/articles/quaternion.md]",
		},
		{
			name: "fraction half and theta",
			in:   "$\\frac{1}{2}\\theta$",
			want: "½θ",
		},
		{
			name: "sqrt single token",
			in:   "$\\sqrt{2}$",
			want: "√2",
		},
		{
			name: "sqrt group bracket",
			in:   "$\\sqrt{x^2+y^2}$",
			want: "√(x²+y²)",
		},
		{
			name: "subscript all glyphs",
			in:   "$q_{jk}$",
			want: "qⱼₖ",
		},
		{
			name: "subscript bracket fallback",
			in:   "$q_{ab}$",
			want: "q_(ab)",
		},
		{
			name: "unknown macro literal",
			in:   `$\foo{x}^2$`,
			want: `\foo{x}²`,
		},
		{
			// A15-1: alias retired — escaped punctuation passes through.
			name: "paren alias",
			in:   `\(x^2\)`,
			want: `\(x^2\)`,
		},
		{
			// A15-1: alias retired — escaped punctuation passes through.
			name: "bracket alias",
			in:   `\[y = mx + b\]`,
			want: `\[y = mx + b\]`,
		},
		{
			name: "aligned environment",
			in:   "$$\\begin{aligned} a &= 1 \\\\ b &= 2 \\end{aligned}$$",
			want: "a = 1\nb = 2",
		},
		{
			name: "superscript bracket fallback",
			in:   "$x^{2n}$",
			want: "x^(2n)",
		},
		{
			// A15-2: \vert maps to ∣ (U+2223), never ASCII | — a bare |
			// inside a table cell lets glamour reshape the row and the
			// cell renders empty.
			name: "vert divides glyph",
			in:   "$\\vert v\\vert^2$",
			want: "∣v∣²",
		},

		// fix2 (A15-3): real-corpus balanced plain span — the '='/'['
		// shape gate (rule 3) admits what hasTeXConstruct alone rejected,
		// so the span no longer stays dollar-wrapped.
		{
			name: "plain bracket span converts",
			in:   `$p=[x,y,z]$`,
			want: `p=[x,y,z]`,
		},
		{
			// fix2 (A15-3): real-corpus single-rune token — the ≤3-rune
			// clause of the shape gate (rule 3) converts $u$ to u.
			name: "single rune token converts",
			in:   "$u$",
			want: "u",
		},
		{
			// fix2 (A15-3): the real line 37 mix — escaped brackets stay
			// inert (A15-1), plain spans convert through the '='/'[' gate
			// and the ≤3-rune token clause. \mathbf keeps its **…** styling
			// (frozen case 8); the rendered page bolds it.
			name: "line 37 mixed prose",
			in:   `的點 p=\[x,y,z\] $p=[x,y,z]$ 繞著軸 u $u$ $\mathbf{u}$ 旋`,
			want: `的點 p=\[x,y,z\] p=[x,y,z] 繞著軸 u u **u** 旋`,
		},
		{
			// fix2 (A15-3): currency digit opener rejected (rule 1) without
			// stealing the following span's opener — the resync (rule 2)
			// lets $x^2$ pair and convert even after a rejected $5.
			name: "currency then math",
			in:   "costs $5 and $x^2$ tests",
			want: "costs $5 and x² tests",
		},
		{
			// fix2 (A15-3): digit opener (rule 1) — $5 is currency even
			// with no other dollar in sight.
			name: "single currency dollar",
			in:   "I paid $5 today",
			want: "I paid $5 today",
		},
		{
			// fix2 (A15-3): inline math never crosses a newline (rule 1) —
			// the stray opener and stray closer both resync (rule 2) and
			// the line stays byte-identical.
			name: "candidate across newline rejected",
			in:   "$a\nb^2 c$ d",
			want: "$a\nb^2 c$ d",
		},
		{
			// fix2 (A15-3) rule 4: apostrophe prime in the superscript
			// glyph table — p^{'} renders p′, not the ^(') fallback.
			// (The fix spec wrote the content bare; mathToUnicode only
			// sees delimited candidates, so the identical content bytes
			// are frozen inside $…$.)
			name: "prime superscript group",
			in:   `$p^{'}$`,
			want: "p′",
		},
		{
			// fix2 (A15-3) rule 4: interior horizontal whitespace of a
			// braced script group is stripped before the glyph mapping —
			// x^{- 1} renders x⁻¹, not x^(- 1). (Content bytes frozen
			// inside $…$; see the prime case above.)
			name: "spaced inverse superscript",
			in:   `$x^{- 1}$`,
			want: "x⁻¹",
		},
		{
			// fix2 (A15-3) rule 4: typographic apostrophe in a script
			// group is a prime — q^{’} renders q′, not the ^(’ ) fallback.
			// (Content bytes frozen inside $…$; see the prime case above.)
			name: "typographic prime superscript",
			in:   `$q^{’}$`,
			want: "q′",
		},
		{
			// fix2 (A15-3): the real line 53 pair. The first span converts
			// through \left (construct gate); the second now converts
			// through the '=' clause (rule 3) — today it stayed
			// dollar-wrapped. The resync (rule 2) hands the inter-span
			// space through untouched. Spec note: the fix spec froze two
			// spaces between the spans ("content's trailing space"), but
			// the input bytes have no trailing space — the closer sits
			// directly after z^{'} — so the compliant output carries
			// exactly the one inter-span space.
			name: "line 53 prime pair",
			in:   `$p^{'} = \left[\right. x^{'} , y^{'} , z^{'}$ $p' = [x', y', z'$`,
			want: `p′ = [ x′ , y′ , z′ p′ = [x′, y′, z′`,
		},
		{
			// fix2 (A15-3): the real line 47 display formula as an inline
			// span — converts through the \left gate with fractions,
			// greek and \mathbf intact. Spec note: the fix spec froze
			// "½θ" and a bare "u"; the compliant output keeps the source
			// space before \theta (only the space AFTER a control word is
			// gobbled — frozen case 19) and \mathbf's **…** styling
			// (frozen case 8).
			name: "line 47 quaternion axis",
			in:   `$q = \left[c o s \left(\frac{1}{2} \theta\right) , s i n \left(\frac{1}{2} \theta\right) \mathbf{u}\right]$`,
			want: `q = [c o s (½ θ) , s i n (½ θ) **u**]`,
		},

		// fix2 (A15-3): construct-less display blocks — the $$…$$ branch
		// runs the same admission + math-shape gate as the inline branch,
		// so the real corpus's dollar-wrapped plain display spans convert
		// too (dollars gone, interior spaces preserved).
		{
			name: "display bracket span converts",
			in:   "$$ v = [0, x, y, z] $$",
			want: " v = [0, x, y, z] ",
		},
		{
			name: "display single rune token converts",
			in:   "$$ θ $$",
			want: " θ ",
		},
		{
			// fix2 (A15-3): display currency-guard identity — the digit
			// opener (rule 1) rejects the candidate and the whole span is
			// emitted byte-identical.
			name: "display currency dollar identity",
			in:   "he paid $$5$$ today",
			want: "he paid $$5$$ today",
		},

		// fix2 (A15-3b): gate rejects markdown-structural bytes — silent-loss
		// class. A construct-less span holding < > * ~ ` stays verbatim with
		// its dollars: stripping the dollars used to hand glamour a raw tag
		// ($a<b$ → <b deleted text), a blockquote opener ($>=x$ at line
		// start), or emphasis paired across spans ($a*b$ and $c*d$).
		{
			name: "gate rejects angle tag loss",
			in:   "if $a<b$ then use x>y",
			want: "if $a<b$ then use x>y",
		},
		{
			name: "gate rejects line-start tag shape",
			in:   "$>=x$ means at least",
			want: "$>=x$ means at least",
		},
		{
			name: "gate rejects cross-span emphasis pairing",
			in:   "given $a*b$ and $c*d$ here",
			want: "given $a*b$ and $c*d$ here",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := mathToUnicode(tc.in)
			if got != tc.want {
				t.Errorf("mathToUnicode(%q)\n =  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}
