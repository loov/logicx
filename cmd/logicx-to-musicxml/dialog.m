// SPDX-License-Identifier: GPL-3.0-or-later

#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

#include "dialog.h"
#include "_cgo_export.h"

// gridValues are the note values both quantization controls offer, off being
// the raw timing Logic recorded.
static NSArray<NSString *> *gridValues(void) {
	return @[@"off", @"1/1", @"1/2", @"1/4", @"1/8", @"1/16", @"1/32", @"1/64"];
}

// startApp brings the process up as a foreground app, which a bare Go binary
// is not until it asks.
static void startApp(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	[NSApp activateIgnoringOtherApps:YES];
}

// copyString hands a Cocoa string to Go, which frees it.
static char *copyString(NSString *string) {
	return strdup(string.UTF8String);
}

// The dialog is laid out by hand, bottom row first, since an accessory view
// this small does not earn a nib.
static const CGFloat dialogWidth = 580;

// addLabel puts a right-aligned caption in front of a control row.
static void addLabel(NSView *view, NSString *text, CGFloat y) {
	NSTextField *label = [NSTextField labelWithString:text];
	label.frame = NSMakeRect(0, y + 4, 180, 18);
	label.alignment = NSTextAlignmentRight;
	[view addSubview:label];
}

// addToServicesMenu asks the system to reread the services every app
// declares, which is all it takes for this one to appear in Logic's menu once
// the app sits where it will stay.
static void addToServicesMenu(void) {
	NSUpdateDynamicServices();

	NSAlert *done = [[NSAlert alloc] init];
	done.messageText = @"Added to the Services menu";
	done.informativeText = @"Logic Pro > Services > Export to MusicXML exports "
		@"the open project. Assign a shortcut to it in System Settings > "
		@"Keyboard > Keyboard Shortcuts > Services.\n\nAn app already running "
		@"picks the menu item up when it is next started.";
	[done runModal];
}

ExportChoice ShowExportDialog(const char *project, const char *destinationPath,
	const char **alternatives, int alternativeCount, int offerMenuItem) {

	ExportChoice choice;
	memset(&choice, 0, sizeof(choice));

	@autoreleasepool {
		startApp();

		NSArray<NSString *> *grids = gridValues();
		NSView *view = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, dialogWidth, 0)];
		__block CGFloat y = 4;

		NSButton *(^addCheck)(NSString *) = ^NSButton *(NSString *title) {
			NSButton *button = [NSButton checkboxWithTitle:title target:nil action:nil];
			button.frame = NSMakeRect(190, y, dialogWidth - 190, 20);
			[view addSubview:button];
			y += 24;
			return button;
		};
		NSSegmentedControl *(^addGrid)(NSString *, NSString *) =
			^NSSegmentedControl *(NSString *title, NSString *selected) {
				NSSegmentedControl *group = [NSSegmentedControl
					segmentedControlWithLabels:grids
					trackingMode:NSSegmentSwitchTrackingSelectOne
					target:nil action:nil];
				group.frame = NSMakeRect(190, y, dialogWidth - 190, 24);
				group.selectedSegment = [grids indexOfObject:selected];
				[view addSubview:group];
				addLabel(view, title, y);
				y += 32;
				return group;
			};

		NSButton *midi = addCheck(@"Also write the unquantized notes as a MIDI file");
		NSButton *realize = addCheck(@"Write chord staves as pitches instead of rhythm slashes");
		NSButton *triplets = addCheck(@"Notate beats played in thirds as triplets");
		triplets.state = NSControlStateValueOn;
		NSSegmentedControl *chordGrid = addGrid(@"Quantize chord symbols to", @"1/4");
		NSSegmentedControl *noteGrid = addGrid(@"Quantize notes to", @"1/16");

		NSPopUpButton *alternative = nil;
		if (alternativeCount > 1) {
			alternative = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(190, y, 160, 26) pullsDown:NO];
			for (int i = 0; i < alternativeCount; i++) {
				[alternative addItemWithTitle:@(alternatives[i])];
			}
			addLabel(view, @"Alternative", y);
			y += 32;
			[view addSubview:alternative];
		}
		view.frameSize = NSMakeSize(dialogWidth, y + 4);

		NSAlert *alert = [[NSAlert alloc] init];
		alert.messageText = @"Export to MusicXML";
		alert.informativeText = @(project);
		alert.accessoryView = view;
		[alert addButtonWithTitle:@"Export…"];
		[alert addButtonWithTitle:@"Cancel"];
		if (offerMenuItem) {
			[alert addButtonWithTitle:@"Add to Logic Pro Menu"];
		}

		// The menu item button leaves the dialog standing: it answers a
		// different question than the one being asked.
		NSModalResponse response;
		while ((response = [alert runModal]) == NSAlertThirdButtonReturn) {
			addToServicesMenu();
		}
		if (response != NSAlertFirstButtonReturn) {
			return choice;
		}

		NSString *destination = @(destinationPath);
		NSSavePanel *panel = [NSSavePanel savePanel];
		panel.message = @"Export the score to";
		panel.nameFieldStringValue = destination.lastPathComponent;
		panel.directoryURL = [NSURL fileURLWithPath:destination.stringByDeletingLastPathComponent];
		panel.allowedFileTypes = @[@"musicxml"];
		[NSApp activateIgnoringOtherApps:YES];
		if ([panel runModal] != NSModalResponseOK) {
			return choice;
		}

		choice.ok = 1;
		choice.alternative = copyString(alternative ? alternative.titleOfSelectedItem : @(alternatives[0]));
		choice.quantize = copyString(grids[noteGrid.selectedSegment]);
		choice.quantizeChords = copyString(grids[chordGrid.selectedSegment]);
		choice.realizeChords = realize.state == NSControlStateValueOn;
		choice.triplets = triplets.state == NSControlStateValueOn;
		choice.midi = midi.state == NSControlStateValueOn;
		choice.destination = copyString(panel.URL.path);
	}
	return choice;
}

void RevealFile(const char *path) {
	@autoreleasepool {
		[[NSWorkspace sharedWorkspace] activateFileViewerSelectingURLs:@[[NSURL fileURLWithPath:@(path)]]];
	}
}

// exportProject runs one project through the Go side and reports what went
// wrong, so a failed drop does not vanish silently.
static void exportProject(NSString *path, BOOL offerMenuItem) {
	char *failure = goExport((char *)path.UTF8String, offerMenuItem);
	if (failure == NULL) {
		return;
	}
	NSAlert *alert = [[NSAlert alloc] init];
	alert.alertStyle = NSAlertStyleCritical;
	alert.messageText = @"Could not export the project";
	alert.informativeText = @(failure);
	free(failure);
	[alert runModal];
}

// Droplet exports the projects dropped on the app icon, and asks for one when
// the app is opened by itself or picked from the Services menu.
@interface Droplet : NSObject <NSApplicationDelegate>
@property(nonatomic, strong) NSMutableArray<NSString *> *dropped;
@property(nonatomic) BOOL working;
@property(nonatomic) BOOL fromMenu;
@end

@implementation Droplet

- (void)application:(NSApplication *)app openFiles:(NSArray<NSString *> *)paths {
	if (self.dropped == nil) {
		self.dropped = [NSMutableArray array];
	}
	[self.dropped addObjectsFromArray:paths];
	[app replyToOpenOrPrint:NSApplicationDelegateReplySuccess];
}

// exportProject:userData:error: is the Services menu item. It takes no input,
// so it does what a plain launch does; the guard is for the launch and the
// service message arriving for the same run.
- (void)exportProject:(NSPasteboard *)pasteboard userData:(NSString *)data error:(NSString **)error {
	self.fromMenu = YES;
	[self run];
}

- (void)applicationDidFinishLaunching:(NSNotification *)note {
	[self run];
}

- (void)run {
	if (self.working) {
		return;
	}
	self.working = YES;

	if (self.dropped.count > 0) {
		for (NSString *path in self.dropped) {
			exportProject(path, NO);
		}
	} else {
		NSOpenPanel *panel = [NSOpenPanel openPanel];
		panel.message = @"Choose a saved Logic Pro project";
		panel.allowedFileTypes = @[@"logicx"];
		panel.allowsMultipleSelection = NO;
		[NSApp activateIgnoringOtherApps:YES];
		if ([panel runModal] == NSModalResponseOK) {
			// Opening the app by hand is where the menu item is worth
			// offering; arriving from the menu means it is already there.
			exportProject(panel.URL.path, !self.fromMenu);
		}
	}
	[NSApp terminate:nil];
}

@end

void RunDroplet(void) {
	@autoreleasepool {
		startApp();
		Droplet *droplet = [[Droplet alloc] init];
		[NSApp setDelegate:droplet];
		[NSApp setServicesProvider:droplet];
		[NSApp run];
	}
}
