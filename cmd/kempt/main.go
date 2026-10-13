// Command kempt turns messy spreadsheets into clean, typed SQL tables.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/FavioJasso/kempt/internal/grid"
	"github.com/FavioJasso/kempt/internal/read"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kempt:", err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	var sheet string
	root := &cobra.Command{Use: "kempt", Short: "Unkempt spreadsheets in, clean typed SQL tables out", SilenceUsage: true, SilenceErrors: true}
	inspect := &cobra.Command{
		Use:   "inspect <file>",
		Short: "Dump the raw grid of every sheet (cells, kinds, merges)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			grids, err := read.File(args[0], read.Options{Sheet: sheet})
			if err != nil {
				return err
			}
			for _, g := range grids {
				dump(cmd.OutOrStdout(), g)
			}
			return nil
		},
	}
	inspect.Flags().StringVar(&sheet, "sheet", "", "only this sheet")
	root.AddCommand(inspect)
	return root
}

func dump(w io.Writer, g *grid.Grid) {
	fmt.Fprintf(w, "sheet %q: %d rows x %d cols (%s)\n", g.Sheet, g.Rows, g.Cols, g.Source)
	for r := 0; r < g.Rows; r++ {
		var cells []string
		for c := 0; c < g.Cols; c++ {
			cell := g.At(r, c)
			if cell.IsEmpty() {
				cells = append(cells, "")
				continue
			}
			cells = append(cells, fmt.Sprintf("%s[%s]", cell.Raw, cell.Kind))
		}
		fmt.Fprintf(w, "%4d | %s\n", r+1, strings.Join(cells, " | "))
	}
	for _, m := range g.Merges {
		fmt.Fprintf(w, "merge %s\n", m.A1())
	}
}
