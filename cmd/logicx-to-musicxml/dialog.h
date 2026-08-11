// SPDX-License-Identifier: GPL-3.0-or-later

#ifndef LOGICX_DIALOG_H
#define LOGICX_DIALOG_H

// ExportChoice is what the export dialog came back with. Every string is
// malloc'd and owned by the caller.
typedef struct {
	int ok;
	char *alternative;
	char *quantize;
	char *quantizeChords;
	int realizeChords;
	int triplets;
	int midi;
	char *destination;
} ExportChoice;

// ShowExportDialog asks for the export settings and where to save. A zero ok
// means the user backed out. A set offerMenuItem adds the button that puts the
// app in Logic's Services menu, for the runs that did not come from there.
ExportChoice ShowExportDialog(const char *project, const char *destination,
	const char **alternatives, int alternativeCount, int offerMenuItem);

// RunDroplet runs the app until the projects dropped on it — or one picked
// from the open panel — have been exported.
void RunDroplet(void);

// RevealFile selects a written file in Finder.
void RevealFile(const char *path);

#endif
