package main

/*
#cgo pkg-config: gtk4 webkitgtk-6.0
#include <gtk/gtk.h>
#include <webkit/webkit.h>

// Returning TRUE from the WebKit context-menu signal marks it handled and
// stops the default menu from being shown. The app renders its own menu in the
// DOM, so the webview's native menu is never wanted. GTK4 dropped the GdkEvent
// parameter from this signal, so the handler takes only the menu and the hit
// test result.
static gboolean posthaste_suppress_context_menu(WebKitWebView *web_view,
                                                WebKitContextMenu *context_menu,
                                                WebKitHitTestResult *hit_test_result,
                                                gpointer user_data) {
    (void)web_view;
    (void)context_menu;
    (void)hit_test_result;
    (void)user_data;
    return TRUE;
}

static void posthaste_guard_context_menu(GtkWidget *widget) {
    if (WEBKIT_IS_WEB_VIEW(widget)) {
        g_signal_connect(G_OBJECT(widget), "context-menu",
                         G_CALLBACK(posthaste_suppress_context_menu), NULL);
    }
    for (GtkWidget *child = gtk_widget_get_first_child(widget);
         child != NULL;
         child = gtk_widget_get_next_sibling(child)) {
        posthaste_guard_context_menu(child);
    }
}

static void posthaste_disable_context_menu(void *window) {
    posthaste_guard_context_menu(GTK_WIDGET(window));
}
*/
import "C"

import "unsafe"

// suppressNativeContextMenu disables WebKitGTK's built-in context menu for the
// webview under the window. It exists because the pinned Wails release only
// implements its DefaultContextMenuDisabled option on Windows; the UI draws
// its own menu on the DOM contextmenu event instead.
//
// The whole widget tree is walked because Wails does not expose the webview
// pointer, only the toplevel GtkWindow.
func suppressNativeContextMenu(window unsafe.Pointer) {
	if window == nil {
		return
	}
	C.posthaste_disable_context_menu(window)
}
