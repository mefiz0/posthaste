package main

/*
#cgo pkg-config: gtk4 webkitgtk-6.0
#include <gtk/gtk.h>
#include <webkit/webkit.h>

// The pinned Wails release documents GTK4 window transparency as a CSS concern
// but never installs that CSS, so the window keeps painting the theme
// background and the UI's frosted surfaces have nothing to sample. Clearing the
// window background (and the webview's, which otherwise falls back to the theme
// colour) lets the desktop show through.
//
// The webview's page background is already transparent: Wails forwards the
// window's RGBA background colour to webkit_web_view_set_background_color.
static void posthaste_apply_transparency(void *window) {
    GtkWidget *widget = GTK_WIDGET(window);

    GtkCssProvider *provider = gtk_css_provider_new();
    gtk_css_provider_load_from_string(
        provider,
        "window.posthaste-transparent,"
        "window.posthaste-transparent.background,"
        "window.posthaste-transparent webview {"
        "  background: none;"
        "}");
    gtk_style_context_add_provider_for_display(
        gtk_widget_get_display(widget),
        GTK_STYLE_PROVIDER(provider),
        GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
    g_object_unref(provider);

    gtk_widget_add_css_class(widget, "posthaste-transparent");
}
*/
import "C"

import "unsafe"

// makeWindowTransparent clears the theme background from the window and its
// webview so translucent UI surfaces can sample the desktop behind the window.
// It exists because the pinned Wails release does not implement the
// transparent background type on GTK4.
func makeWindowTransparent(window unsafe.Pointer) {
	if window == nil {
		return
	}
	C.posthaste_apply_transparency(window)
}
