//go:build gtk3

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

// Returning TRUE from the WebKit context-menu signal marks it handled and
// stops the default menu from being shown. The app renders its own menu in the
// DOM, so the webview's native menu is never wanted.
static gboolean posthaste_suppress_context_menu(WebKitWebView *web_view,
                                                WebKitContextMenu *context_menu,
                                                GdkEvent *event,
                                                WebKitHitTestResult *hit_test_result,
                                                gpointer user_data) {
    (void)web_view;
    (void)context_menu;
    (void)event;
    (void)hit_test_result;
    (void)user_data;
    return TRUE;
}

static void posthaste_guard_context_menu(GtkWidget *widget) {
    if (WEBKIT_IS_WEB_VIEW(widget)) {
        g_signal_connect(G_OBJECT(widget), "context-menu",
                         G_CALLBACK(posthaste_suppress_context_menu), NULL);
    }
    if (GTK_IS_CONTAINER(widget)) {
        GList *children = gtk_container_get_children(GTK_CONTAINER(widget));
        for (GList *item = children; item != NULL; item = item->next) {
            posthaste_guard_context_menu(GTK_WIDGET(item->data));
        }
        g_list_free(children);
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
