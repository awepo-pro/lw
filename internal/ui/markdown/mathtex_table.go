package markdown

// mathMacroGlyphs maps TeX macro names (without the leading backslash) to
// their unicode-markdown rendering. Single non-letter tokens ("\{" and
// friends) are keyed by the token itself. BMP codepoints only — the
// Mathematical Alphanumeric block (U+1D400+) is never emitted.
var mathMacroGlyphs = map[string]string{
	// Greek lowercase
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ",
	"epsilon": "ε", "varepsilon": "ε", "zeta": "ζ", "eta": "η",
	"theta": "θ", "vartheta": "ϑ", "iota": "ι", "kappa": "κ",
	"lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "omicron": "ο",
	"pi": "π", "varpi": "ϖ", "rho": "ρ", "varrho": "ϱ", "sigma": "σ",
	"varsigma": "ς", "tau": "τ", "upsilon": "υ", "phi": "φ",
	"varphi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	// Greek uppercase
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ",
	"Xi": "Ξ", "Pi": "Π", "Sigma": "Σ", "Upsilon": "Υ", "Phi": "Φ",
	"Psi": "Ψ", "Omega": "Ω",
	// Arrows
	"to": "→", "rightarrow": "→", "leftarrow": "←", "gets": "←",
	"leftrightarrow": "↔", "Rightarrow": "⇒", "Leftarrow": "⇐",
	"Leftrightarrow": "⇔", "mapsto": "↦", "uparrow": "↑",
	"downarrow": "↓", "updownarrow": "↕", "Uparrow": "⇑",
	"Downarrow": "⇓", "Updownarrow": "⇕", "nearrow": "↗",
	"searrow": "↘", "swarrow": "↙", "nwarrow": "↖",
	"hookrightarrow": "↪", "hookleftarrow": "↩",
	"rightharpoonup": "⇀", "leftharpoonup": "↼",
	"implies": "⇒", "impliedby": "⇐", "iff": "⇔",
	// Binary operators
	"times": "×", "div": "÷", "cdot": "⋅", "pm": "±", "mp": "∓",
	"ast": "∗", "star": "⋆", "circ": "∘", "bullet": "•",
	"oplus": "⊕", "ominus": "⊖", "otimes": "⊗", "oslash": "⊘",
	"odot": "⊙", "dagger": "†", "ddagger": "‡", "amalg": "⨿",
	"cup": "∪", "cap": "∩", "wedge": "∧", "vee": "∨",
	"setminus": "∖", "sqcup": "⊔", "sqcap": "⊓", "uplus": "⊎",
	// Big operators
	"sum": "∑", "prod": "∏", "coprod": "∐", "int": "∫",
	"iint": "∬", "iiint": "∭", "oint": "∮", "bigcup": "⋃",
	"bigcap": "⋂", "bigvee": "⋁", "bigwedge": "⋀",
	"biguplus": "⨄", "bigsqcup": "⨆", "bigodot": "⨀",
	"bigoplus": "⨁", "bigotimes": "⨂", "bigcirc": "◯",
	// Relations
	"leq": "≤", "le": "≤", "geq": "≥", "ge": "≥", "neq": "≠",
	"ne": "≠", "approx": "≈", "equiv": "≡", "sim": "∼",
	"simeq": "≃", "cong": "≅", "asymp": "≍", "propto": "∝",
	"ll": "≪", "gg": "≫", "prec": "≺", "succ": "≻",
	"preceq": "⪯", "succeq": "⪰", "subset": "⊂", "supset": "⊃",
	"subseteq": "⊆", "supseteq": "⊇", "nsubseteq": "⊈",
	"nsupseteq": "⊉", "sqsubseteq": "⊑", "sqsupseteq": "⊒",
	"in": "∈", "ni": "∋", "notin": "∉", "vdash": "⊢",
	"dashv": "⊣", "perp": "⊥", "parallel": "∥", "mid": "∣",
	"smile": "⌣", "frown": "⌢",
	// Logic
	"neg": "¬", "lnot": "¬", "land": "∧", "lor": "∨",
	"forall": "∀", "exists": "∃", "nexists": "∄",
	"therefore": "∴", "because": "∵",
	// Miscellaneous symbols
	"infty": "∞", "nabla": "∇", "partial": "∂", "emptyset": "∅",
	"varnothing": "∅", "angle": "∠", "measuredangle": "∡",
	"triangle": "△", "square": "□", "blacksquare": "■",
	"diamond": "◇", "Diamond": "◇", "bigstar": "★",
	"checkmark": "✓", "degree": "°", "prime": "′", "hbar": "ℏ",
	"ell": "ℓ", "Re": "ℜ", "Im": "ℑ", "aleph": "ℵ", "beth": "ℶ",
	"gimel": "ℷ", "daleth": "ℸ", "wp": "℘",
	// Dots
	"dots": "…", "ldots": "…", "cdots": "⋯", "vdots": "⋮",
	"ddots": "⋱",
	// Delimiters (also usable after \left / \right). "vert" must not emit
	// ASCII | — A15-2: a bare | inside a table cell lets glamour's table
	// parser reshape the row and drop the cell. | and Vert keep ‖ (U+2016).
	"{": "{", "}": "}", "|": "‖", "vert": "∣", "Vert": "‖",
	"backslash": "\\", "langle": "⟨", "rangle": "⟩",
	"lceil": "⌈", "rceil": "⌉", "lfloor": "⌊", "rfloor": "⌋",
}

// mathFractionGlyphs maps "num/den" (converted) to a vulgar-fraction glyph.
// Fractions absent from the table render linearly as (a)/(b).
var mathFractionGlyphs = map[string]string{
	"1/2": "½", "1/3": "⅓", "2/3": "⅔", "1/4": "¼", "3/4": "¾",
	"1/5": "⅕", "2/5": "⅖", "3/5": "⅗", "4/5": "⅘", "1/6": "⅙",
	"5/6": "⅚", "1/8": "⅛", "3/8": "⅜", "5/8": "⅝", "7/8": "⅞",
}

// mathSupGlyphs: superscript glyphs for every character that has one.
// Letters deliberately absent — a group containing any unmapped character
// falls back to bracket form (§F.1 case 18).
var mathSupGlyphs = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	// fix2 (A15-3) rule 4: real-corpus primes in script groups — p^{'}
	// and q^{’} render p′/q′, never the ^(') / ^(’ ) fallback shapes.
	'\'': '′', '’': '′',
}

// mathSubGlyphs: subscript glyphs. Letters limited to the pinned ⱼ and ₖ;
// any other character sends the whole group to bracket form (§F.1 case 13).
var mathSubGlyphs = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	'j': 'ⱼ', 'k': 'ₖ',
}

// mathbbGlyphs: the BMP Letterlike subset of \mathbb (§F.1 pin).
var mathbbGlyphs = map[rune]rune{
	'C': 'ℂ', 'N': 'ℕ', 'Q': 'ℚ', 'R': 'ℝ', 'Z': 'ℤ',
}
