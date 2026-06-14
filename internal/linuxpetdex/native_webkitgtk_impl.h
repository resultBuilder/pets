#include <gtk/gtk.h>
#include <stdint.h>
#include <stdlib.h>
#include <webkit2/webkit2.h>

extern void codexPetsLinuxBridgeMessage(uintptr_t handle, char* raw);
extern void codexPetsLinuxLoaded(uintptr_t handle);
extern void codexPetsLinuxClosed(uintptr_t handle);

typedef struct PetdexWindow {
  GtkWidget* window;
  WebKitWebView* web_view;
  uintptr_t handle;
  gboolean loaded_sent;
} PetdexWindow;

typedef struct PetdexEvalData {
  PetdexWindow* window;
  char* script;
} PetdexEvalData;

int petdex_gtk_init(void) {
  int argc = 0;
  char** argv = NULL;
  return gtk_init_check(&argc, &argv) ? 1 : 0;
}

static void petdex_on_message(
  WebKitUserContentManager* manager,
  WebKitJavascriptResult* result,
  gpointer user_data
) {
  (void)manager;
  PetdexWindow* window = (PetdexWindow*)user_data;
  if (window == NULL || result == NULL) {
    return;
  }
  JSCValue* value = webkit_javascript_result_get_js_value(result);
  if (value == NULL) {
    return;
  }
  char* message = jsc_value_to_string(value);
  if (message == NULL) {
    return;
  }
  codexPetsLinuxBridgeMessage(window->handle, message);
  g_free(message);
}

static void petdex_on_load_changed(
  WebKitWebView* web_view,
  WebKitLoadEvent load_event,
  gpointer user_data
) {
  (void)web_view;
  PetdexWindow* window = (PetdexWindow*)user_data;
  if (window == NULL || window->loaded_sent || load_event != WEBKIT_LOAD_FINISHED) {
    return;
  }
  window->loaded_sent = TRUE;
  codexPetsLinuxLoaded(window->handle);
}

static void petdex_on_destroy(GtkWidget* widget, gpointer user_data) {
  (void)widget;
  PetdexWindow* window = (PetdexWindow*)user_data;
  if (window != NULL) {
    window->window = NULL;
    window->web_view = NULL;
    codexPetsLinuxClosed(window->handle);
  }
  gtk_main_quit();
}

PetdexWindow* petdex_window_new(uintptr_t handle, const char* init_script, const char* uri) {
  PetdexWindow* window = g_new0(PetdexWindow, 1);
  window->handle = handle;

  WebKitUserContentManager* manager = webkit_user_content_manager_new();
  WebKitUserScript* script = webkit_user_script_new(
    init_script,
    WEBKIT_USER_CONTENT_INJECT_TOP_FRAME,
    WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START,
    NULL,
    NULL
  );
  webkit_user_content_manager_add_script(manager, script);
  webkit_user_script_unref(script);
  webkit_user_content_manager_register_script_message_handler(manager, "codexPets");
  g_signal_connect(manager, "script-message-received::codexPets", G_CALLBACK(petdex_on_message), window);

  window->window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  gtk_window_set_title(GTK_WINDOW(window->window), "Petdex");
  gtk_window_set_default_size(GTK_WINDOW(window->window), 980, 640);
  gtk_widget_set_size_request(window->window, 820, 560);

  window->web_view = WEBKIT_WEB_VIEW(webkit_web_view_new_with_user_content_manager(manager));
  g_object_unref(manager);
  gtk_container_add(GTK_CONTAINER(window->window), GTK_WIDGET(window->web_view));

  g_signal_connect(window->window, "destroy", G_CALLBACK(petdex_on_destroy), window);
  g_signal_connect(window->web_view, "load-changed", G_CALLBACK(petdex_on_load_changed), window);

  gtk_widget_show_all(window->window);
  webkit_web_view_load_uri(window->web_view, uri);
  return window;
}

static gboolean petdex_eval_on_main(gpointer user_data) {
  PetdexEvalData* data = (PetdexEvalData*)user_data;
  if (data != NULL && data->window != NULL && data->window->web_view != NULL && data->script != NULL) {
    webkit_web_view_run_javascript(data->window->web_view, data->script, NULL, NULL, NULL);
  }
  if (data != NULL) {
    g_free(data->script);
    g_free(data);
  }
  return G_SOURCE_REMOVE;
}

void petdex_window_eval(PetdexWindow* window, const char* script) {
  if (window == NULL || script == NULL) {
    return;
  }
  PetdexEvalData* data = g_new0(PetdexEvalData, 1);
  data->window = window;
  data->script = g_strdup(script);
  g_main_context_invoke(NULL, petdex_eval_on_main, data);
}

static gboolean petdex_close_on_main(gpointer user_data) {
  PetdexWindow* window = (PetdexWindow*)user_data;
  if (window != NULL && window->window != NULL) {
    gtk_window_close(GTK_WINDOW(window->window));
  }
  return G_SOURCE_REMOVE;
}

void petdex_window_close(PetdexWindow* window) {
  if (window == NULL) {
    return;
  }
  g_main_context_invoke(NULL, petdex_close_on_main, window);
}

void petdex_window_free(PetdexWindow* window) {
  if (window != NULL) {
    g_free(window);
  }
}

void petdex_gtk_main(void) {
  gtk_main();
}
