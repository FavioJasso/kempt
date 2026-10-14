# Renders the README demo GIF:  vhs demo.tape
# Requires kempt on PATH (go install ./cmd/kempt) and runs from the repo root.

Output demo.gif

Require kempt

Set Shell "bash"
Set FontSize 15
Set Width 1280
Set Height 760
Set Padding 18
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "cd testdata/fixtures && rm -f /tmp/kempt-demo.db /tmp/kempt-report.json && clear" Enter
Show

Type "# A city budget sheet: titles, a two-row header, subtotals, footnotes" Enter
Sleep 600ms
Type "kempt inspect budget_report.xlsx | head -34" Enter
Sleep 6s

Type "clear" Enter
Type "kempt ddl budget_report.xlsx --dialect postgres" Enter
Sleep 4s

Type "clear" Enter
Type "kempt load budget_report.xlsx --dsn /tmp/kempt-demo.db --report /tmp/kempt-report.json" Enter
Sleep 2s
Type "kempt inspect messy_values.xlsx --rows 4 | head -40" Enter
Sleep 6s

Hide
Type "rm -f /tmp/kempt-demo.db /tmp/kempt-report.json" Enter
