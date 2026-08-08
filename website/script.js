(() => {
  document.documentElement.classList.add("js");

  const menuButton = document.querySelector(".menu-button");
  const siteNav = document.getElementById("site-nav");

  if (!menuButton || !siteNav) {
    return;
  }

  const setMenuOpen = (isOpen) => {
    siteNav.classList.toggle("is-open", isOpen);
    menuButton.setAttribute("aria-expanded", String(isOpen));
  };

  menuButton.addEventListener("click", () => {
    setMenuOpen(!siteNav.classList.contains("is-open"));
  });

  siteNav.addEventListener("click", (event) => {
    if (event.target.closest("a")) {
      setMenuOpen(false);
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && siteNav.classList.contains("is-open")) {
      setMenuOpen(false);
      menuButton.focus();
    }
  });

  if (typeof window.matchMedia === "function") {
    const wideLayout = window.matchMedia("(min-width: 761px)");
    const closeMenu = () => setMenuOpen(false);

    if (typeof wideLayout.addEventListener === "function") {
      wideLayout.addEventListener("change", closeMenu);
    } else if (typeof wideLayout.addListener === "function") {
      wideLayout.addListener(closeMenu);
    }
  }
})();
