// Package apptest runs a product in its own tests as production runs it: on
// a data directory, where the framework seals what it keeps at rest, a transaction
// writes files and a rollback writes them back, a store's retention erases
// and folds what it overwrote, and a restart reads everything back — none
// of which a product kept in memory (app.InMemory) ever meets: the framework seals
// nothing in memory.
//
// A suite keeps most of its tests in memory, fast and hermetic, and runs on
// disk those that are about what rests there:
//
//	func TestNothingPersonalRestsInClear(t *testing.T) {
//		disk := apptest.NewDisk(t)
//		app := start(t, disk) // the suite's own helper, in memory unless told otherwise
//		fileReport(t, app, "ann@example.com")
//		if files := disk.Holding(t, "ann@example.com"); len(files) > 0 {
//			t.Errorf("in clear on disk: %v", files)
//		}
//	}
//
// A Disk is an AppOption: app.DataDir(disk.Dir), which wins over a
// app.InMemory given before it. Given to the product started again — once
// the first app stopped — it is the same data and the same secrets: a
// restart.
package apptest
