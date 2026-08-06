(() => {
  document.documentElement.classList.add("js");

  const menuButton = document.querySelector(".menu-button");
  const siteNav = document.getElementById("site-nav");

  if (menuButton && siteNav) {
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
      window.matchMedia("(min-width: 761px)").addEventListener("change", () => {
        setMenuOpen(false);
      });
    }
  }

  const motionQuery = window.matchMedia?.("(prefers-reduced-motion: reduce)");
  if (motionQuery?.matches || !("IntersectionObserver" in window)) {
    return;
  }

  document.documentElement.classList.add("enhanced");
  const observer = new IntersectionObserver((entries) => {
    entries.forEach((entry) => {
      if (entry.isIntersecting) {
        entry.target.classList.add("is-visible");
        observer.unobserve(entry.target);
      }
    });
  }, {
    rootMargin: "0px 0px -8%",
    threshold: 0.12,
  });

  document.querySelectorAll("[data-reveal]").forEach((element) => {
    observer.observe(element);
  });
})();
