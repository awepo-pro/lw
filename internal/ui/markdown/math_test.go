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
